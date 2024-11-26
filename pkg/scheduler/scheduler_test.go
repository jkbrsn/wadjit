package scheduler

import (
	"context"
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
// TODO: write test where tasks are added mid-run

func TestMain(m *testing.M) {
	zerolog.TimeFieldFormat = time.RFC3339Nano
	log.Logger = log.Output(zerolog.ConsoleWriter{Out: os.Stderr, TimeFormat: "15:04:05.999"}).Level(zerolog.DebugLevel)
	os.Exit(m.Run())
}

func TestNewScheduler(t *testing.T) {
	taskChan := make(chan<- Task)
	scheduler := NewScheduler(taskChan)
	defer scheduler.Stop()

	assert.NotNil(t, scheduler.jobQueue, "Expected job queue to be non-nil")

	assert.Equal(t, scheduler.taskChan, taskChan, "Expected task channel to be set correctly")
}

func TestSchedulerStop(t *testing.T) {
	taskChan := make(chan Task)
	scheduler := NewScheduler(taskChan)
	scheduler.Start()

	// Immediately stop the scheduler
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
	taskChan := make(chan Task, 1)
	scheduler := NewScheduler(taskChan)
	defer scheduler.Stop()

	testTask := MockTask{ID: "test-task", cadence: 100 * time.Millisecond}
	scheduler.AddTask(testTask, testTask.ID)

	assert.Equal(t, 1, scheduler.jobQueue.Len(), "Expected job queue length to be 1, got %d", scheduler.jobQueue.Len())

	job := scheduler.jobQueue[0]
	assert.Equal(t, 1, len(job.Tasks), "Expected job to have 1 task, got %d", len(job.Tasks))
	assert.Equal(t, testTask, job.Tasks[0], "Expected the task in the job to be the test task")
}

func TestAddJob(t *testing.T) {
	taskChan := make(chan Task, 2)
	scheduler := NewScheduler(taskChan)
	defer scheduler.Stop()

	mockTasks := []MockTask{
		{ID: "task1", cadence: 100 * time.Millisecond},
		{ID: "task2", cadence: 100 * time.Millisecond},
	}
	// Explicitly convert []MockTask to []Task to satisfy the AddJob method signature, since slices are not covariant in Go
	tasks := make([]Task, len(mockTasks))
	for i, task := range mockTasks {
		tasks[i] = task
	}
	scheduler.AddJob(tasks, 100*time.Millisecond, "group1")

	// Assert that the job was added
	assert.Equal(t, 1, scheduler.jobQueue.Len(), "Expected job queue length to be 1, got %d", scheduler.jobQueue.Len())
	job := scheduler.jobQueue[0]
	assert.Equal(t, 2, len(job.Tasks), "Expected job to have 2 tasks, got %d", len(job.Tasks))
}

func TestRemoveJob(t *testing.T) {
	taskChan := make(chan Task, 2)
	scheduler := NewScheduler(taskChan)
	defer scheduler.Stop()

	mockTasks := []MockTask{
		{ID: "task1", cadence: 100 * time.Millisecond},
		{ID: "task2", cadence: 100 * time.Millisecond},
	}
	// Explicitly convert []MockTask to []Task to satisfy the AddJob method signature, since slices are not covariant in Go
	tasks := make([]Task, len(mockTasks))
	for i, task := range mockTasks {
		tasks[i] = task
	}
	scheduler.AddJob(tasks, 100*time.Millisecond, "group1")

	// Assert that the job was added
	assert.Equal(t, 1, scheduler.jobQueue.Len(), "Expected job queue length to be 1, got %d", scheduler.jobQueue.Len())
	job := scheduler.jobQueue[0]
	assert.Equal(t, 2, len(job.Tasks), "Expected job to have 2 tasks, got %d", len(job.Tasks))

	// Remove the job
	scheduler.RemoveJob("group1")

	// Assert that the job was removed
	assert.Equal(t, 0, scheduler.jobQueue.Len(), "Expected job queue length to be 0, got %d", scheduler.jobQueue.Len())
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
	resultChan := make(chan Result, 4) // Make room in buffered channel for multiple results, since we're not receiving them in this test

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

// TODO: clean test up, removing some logs
func TestAddTaskDuringExecution(t *testing.T) {
	taskChan := make(chan Task, 1)
	resultChan := make(chan Result, 1)

	workerPool := NewWorkerPool(resultChan, taskChan, 10)
	workerPool.Start()
	defer workerPool.Stop()
	scheduler := NewScheduler(taskChan)
	scheduler.Start()
	defer scheduler.Stop()

	// Consume resultChan to prevent workers from blocking
	go func() {
		for range resultChan {
			// Do nothing
		}
	}()

	// Dedicated channels for task execution signals
	task1Executed := make(chan struct{})
	defer close(task1Executed)

	// Use a context to cancel the test
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	trackTasks := map[string]bool{
		"task1": false,
		"task2": false,
	}
	var mu sync.Mutex

	// Create test tasks
	testTask1 := &MockTask{
		ID:      "test-add-mid-execution-task-1",
		cadence: 15 * time.Millisecond,
		executeFunc: func() {
			select {
			case <-ctx.Done():
				t.Log("Task 1 cancelled")
				return
			default:
				t.Log("Executing task 1")
				task1Executed <- struct{}{}
				mu.Lock()
				trackTasks["task1"] = true
				mu.Unlock()
				t.Logf("Executed task 1, %v", time.Now())
			}
		},
	}
	testTask2 := &MockTask{
		ID:      "test-add-mid-execution-task-2",
		cadence: 10 * time.Millisecond,
		executeFunc: func() {
			select {
			case <-ctx.Done():
				t.Log("Task 2 cancelled")
				return
			default:
				t.Log("Executing task 2")
				mu.Lock()
				trackTasks["task2"] = true
				mu.Unlock()
				t.Logf("Executed task 2, %v", time.Now())
			}
		},
	}

	// Schedule first task
	t.Log("Scheduling task 1")
	scheduler.AddTask(testTask1, testTask1.ID)
	start := time.Now()

	select {
	case <-task1Executed:
		// Task executed as expected
		t.Logf("Task 1 executed after %v", time.Since(start))
	case <-time.After(20 * time.Millisecond):
		t.Fatalf("Task 1 did not execute within the expected time frame (40ms), %v", time.Since(start))
	}

	// Consume task1Executed to prevent it from blocking
	go func() {
		for range task1Executed {
			// Do nothing
		}
	}()

	// Schedule second task after we've had at least one execution of the first task
	t.Logf("Scheduling task 2, %v", time.Since(start))
	scheduler.AddTask(testTask2, testTask2.ID)

	// Sleep enough time to make sure both tasks have executed at least once
	time.Sleep(30 * time.Millisecond)

	cancel()
	scheduler.Stop()

	// Check that both tasks have executed
	mu.Lock()
	for msg, executed := range trackTasks {
		if !executed {
			t.Fatalf("Task '%s' did not execute, %v", msg, time.Since(start))
		}
	}
	mu.Unlock()
}

func TestConcurrentAddTask(t *testing.T) {
	// Deactivate debug logs for this test
	log.Logger = log.Output(zerolog.ConsoleWriter{Out: os.Stderr, TimeFormat: "15:04:05.999"}).Level(zerolog.InfoLevel)

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

// TODO: add test for concurrently running tasks

func TestZeroCadenceTask(t *testing.T) {
	taskChan := make(chan Task)
	scheduler := NewScheduler(taskChan)
	scheduler.Start()
	defer scheduler.Stop()

	testTask := NewDefaultTask("zero-cadence-task", 0)
	scheduler.AddTask(testTask, testTask.ID)

	// Expect the task to not execute
	select {
	case <-taskChan:
		// Success
		t.Fatal("Task with zero cadence should not execute")
	case <-time.After(50 * time.Millisecond):
		// After 50ms, the task would have executed if it was scheduled
		log.Debug().Msg("Task with zero cadence never executed")
	}
}
