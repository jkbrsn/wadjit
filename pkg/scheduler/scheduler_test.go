package scheduler

import (
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"github.com/stretchr/testify/assert"
)

type MockTask struct {
	ID      string
	cadence time.Duration

	executeFunc func()
}

func (mt MockTask) Cadence() time.Duration {
	return mt.cadence
}

func (mt MockTask) Execute() Result {
	log.Debug().Msgf("Executing MockTask with ID: %s", mt.ID)
	if mt.executeFunc != nil {
		mt.executeFunc()
	}
	return Result{Success: true}
}

// TODO: write test comparing what happens when different channel types are used for taskChan

func TestMain(m *testing.M) {
	log.Logger = log.Output(zerolog.ConsoleWriter{Out: os.Stderr}).Level(zerolog.DebugLevel)
	os.Exit(m.Run())
}

func TestNewScheduler(t *testing.T) {
	taskChan := make(chan<- Task)
	scheduler := NewScheduler(taskChan)

	assert.NotNil(t, scheduler.jobQueue, "Expected job queue to be non-nil")

	assert.Equal(t, scheduler.taskChannel, taskChan, "Expected task channel to be set correctly")
}

func TestSchedulerStop(t *testing.T) {
	taskChan := make(chan Task)
	scheduler := NewScheduler(taskChan)
	scheduler.Start()

	scheduler.Stop()

	// Attempt to add a task after stopping
	testTask := NewDefaultTask("test-task", 100*time.Millisecond)
	scheduler.AddTask(testTask, testTask.ID)

	// Since the scheduler is stopped, the task should not be scheduled
	if scheduler.jobQueue.Len() != 1 {
		t.Fatalf("Expected job queue length to be 1, got %d", scheduler.jobQueue.Len())
	}

	// Wait some time to see if the task executes
	select {
	case <-taskChan:
		t.Fatal("Did not expect any tasks to be executed after scheduler is stopped")
	case <-time.After(200 * time.Millisecond):
		// No tasks executed, as expected
	}
}
func TestAddTask(t *testing.T) {
	taskChan := make(chan<- Task, 1)
	scheduler := NewScheduler(taskChan)

	testTask := MockTask{ID: "test-task", cadence: 100 * time.Millisecond}
	scheduler.AddTask(testTask, testTask.ID)

	assert.Equal(t, 1, scheduler.jobQueue.Len(), "Expected job queue length to be 1, got %d", scheduler.jobQueue.Len())

	job := scheduler.jobQueue[0]
	assert.Equal(t, 1, len(job.Tasks), "Expected job to have 1 task, got %d", len(job.Tasks))
	assert.Equal(t, testTask, job.Tasks[0], "Expected the task in the job to be the test task")
}

func TestAddTasks(t *testing.T) {
	taskChan := make(chan<- Task, 2)
	scheduler := NewScheduler(taskChan)

	mockTasks := []MockTask{
		{ID: "task1", cadence: 100 * time.Millisecond},
		{ID: "task2", cadence: 100 * time.Millisecond},
	}
	// Explicitly convert []MockTask to []Task to satisfy the AddTasks method signature, since slices are not covariant in Go
	tasks := make([]Task, len(mockTasks))
	for i, task := range mockTasks {
		tasks[i] = task
	}
	scheduler.AddTasks(tasks, 100*time.Millisecond, "group1")

	assert.Equal(t, 1, scheduler.jobQueue.Len(), "Expected job queue length to be 1, got %d", scheduler.jobQueue.Len())

	job := scheduler.jobQueue[0]
	assert.Equal(t, 2, len(job.Tasks), "Expected job to have 2 tasks, got %d", len(job.Tasks))
}

func TestTaskExecution(t *testing.T) {
	taskChan := make(chan Task, 1)
	resultChan := make(chan Result, 1)

	workerPool := NewWorkerPool(resultChan, taskChan, 10)
	workerPool.Start()
	defer workerPool.Stop()
	scheduler := NewScheduler(taskChan)
	scheduler.Start()
	defer scheduler.Stop()

	var wg sync.WaitGroup
	wg.Add(1)

	// Use a channel to receive execution times
	executionTimes := make(chan time.Time, 1)

	// Create a test task
	testTask := &MockTask{
		ID:      "test-execution-task",
		cadence: 100 * time.Millisecond,
		executeFunc: func() {
			log.Debug().Msg("Executing TestTaskExecution task")
			executionTimes <- time.Now()
			wg.Done()
		},
	}

	scheduler.AddTask(testTask, testTask.ID)

	select {
	case execTime := <-executionTimes:
		elapsed := time.Since(execTime)
		assert.Greater(t, 150*time.Millisecond, elapsed, "Task executed after %v, expected around 100ms", elapsed)
	case <-time.After(200 * time.Millisecond):
		t.Fatal("Task did not execute in expected time")
	}
}

func TestTaskRescheduling(t *testing.T) {
	taskChan := make(chan Task, 1)
	resultChan := make(chan Result, 1)

	workerPool := NewWorkerPool(resultChan, taskChan, 10)
	workerPool.Start()
	defer workerPool.Stop()
	scheduler := NewScheduler(taskChan)
	scheduler.Start()
	defer scheduler.Stop()

	var executionTimes []time.Time
	var mu sync.Mutex

	// Create a test task that records execution times
	mockTask := &MockTask{
		ID:      "test-rescheduling-task",
		cadence: 100 * time.Millisecond,
		executeFunc: func() {
			mu.Lock()
			executionTimes = append(executionTimes, time.Now())
			mu.Unlock()
		},
	}

	scheduler.AddTask(mockTask, mockTask.ID)

	// Wait for the task to execute multiple times
	// Sleeing for 350ms should allow for about 3 executions
	time.Sleep(350 * time.Millisecond)

	// Stop the scheduler to prevent further executions
	scheduler.Stop()

	mu.Lock()
	execCount := len(executionTimes)
	mu.Unlock()

	assert.LessOrEqual(t, 3, execCount, "Expected at least 3 executions, got %d", execCount)

	// Check that the executions occurred at roughly the correct intervals
	mu.Lock()
	for i := 1; i < len(executionTimes); i++ {
		diff := executionTimes[i].Sub(executionTimes[i-1])
		if diff < 90*time.Millisecond || diff > 110*time.Millisecond {
			t.Fatalf("Execution interval out of expected range: %v", diff)
		}
	}
	mu.Unlock()
}

func TestConcurrentAddTask(t *testing.T) {
	taskChan := make(chan Task)
	scheduler := NewScheduler(taskChan)
	scheduler.Start()
	defer scheduler.Stop()

	var wg sync.WaitGroup
	numGoroutines := 20
	numTasksPerGoroutine := 100

	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < numTasksPerGoroutine; j++ {
				taskID := fmt.Sprintf("task-%d-%d", id, j)
				task := NewDefaultTask(taskID, 100*time.Millisecond)
				scheduler.AddTask(task, taskID)
			}
		}(i)
	}

	wg.Wait()

	// Verify that all tasks are scheduled
	expectedTasks := numGoroutines * numTasksPerGoroutine
	assert.Equal(t, expectedTasks, scheduler.jobQueue.Len(), "Expected job queue length to be %d, got %d", expectedTasks, scheduler.jobQueue.Len())
}
