package scheduler

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

type MockTask struct {
	ID      string
	cadence time.Duration
}

func (mt MockTask) Cadence() time.Duration {
	return mt.cadence
}

func (mt MockTask) Execute() Result {
	return Result{}
}

func TestNewScheduler(t *testing.T) {
	taskChan := make(chan<- Task)
	scheduler := NewScheduler(taskChan)

	assert.NotNil(t, scheduler.jobQueue, "Expected job queue to be non-nil")

	assert.Equal(t, scheduler.taskChannel, taskChan, "Expected task channel to be set correctly")
}

func TestAddTask(t *testing.T) {
	taskChan := make(chan Task, 1)
	scheduler := NewScheduler(taskChan)

	testTask := MockTask{"test-task", 100 * time.Millisecond}
	scheduler.AddTask(testTask)

	assert.Equal(t, 1, scheduler.jobQueue.Len(), "Expected job queue length to be 1, got %d", scheduler.jobQueue.Len())

	job := scheduler.jobQueue[0]
	assert.Equal(t, 1, len(job.Tasks), "Expected job to have 1 task, got %d", len(job.Tasks))
	assert.Equal(t, testTask, job.Tasks[0], "Expected the task in the job to be the test task")
}

func TestAddTasks(t *testing.T) {
	taskChan := make(chan Task, 2)
	scheduler := NewScheduler(taskChan)

	mockTasks := []MockTask{
		{"task1", 100 * time.Millisecond},
		{"task2", 100 * time.Millisecond},
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
