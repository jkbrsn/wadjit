package scheduling

import (
	"sync"
	"sync/atomic"
	"time"
)

type Task struct {
	// TODO: consider creating a unique TaskID type
	ID       string // Unique identifier, e.g., endpoint URL
	// TODO: also consider a GroupID for grouping tasks
	// TODO: consider a TaskType for categorizing tasks, e.g. WS vs HTTP
	Cadence  time.Duration
	Execute  func() // Function to execute, e.g., making an HTTP request
	NextExec time.Time
}

type TaskGroup struct {
    Tasks    []Task
    ready    chan struct{} // Channel to signal readiness for execution
    waitGroup sync.WaitGroup
}

type WorkerPool struct {
	Tasks    chan Task
	Workers  int
    active   int32 // Use atomic operations to manage this counter
	stopChan chan struct{} // Used to signal stopping of workers
}

func (wp *WorkerPool) start() {
    for i := 0; i < wp.Workers; i++ {
        go func() {
            for {
                select {
                case task := <-wp.Tasks:
                    atomic.AddInt32(&wp.active, 1) // Increment active workers
                    task.Execute()
                    atomic.AddInt32(&wp.active, -1) // Decrement active workers
                case <-wp.stopChan:
                    return
                }
            }
        }()
    }
}

func (tg *TaskGroup) ExecuteTogether() {
    // Signal readiness and wait for all tasks to be ready
    close(tg.ready)
    tg.waitGroup.Wait()
}

// Submit adds a task to the worker pool for execution.
func (wp *WorkerPool) Submit(task Task) {
	wp.Tasks <- task
}

// SubmitGroup adds a task group to the worker pool for simultaneous execution.
func (wp *WorkerPool) SubmitGroup(tg *TaskGroup) {
    tg.waitGroup.Add(len(tg.Tasks))
    for _, task := range tg.Tasks {
        go func(t Task) {
            <-tg.ready // Wait for the signal to start
            t.Execute()
            tg.waitGroup.Done()
        }(task)
    }
}

// Stop signals the worker pool to stop processing tasks and exit.
func (wp *WorkerPool) Stop() {
	close(wp.stopChan)
}

func NewTaskGroup(tasks []Task) *TaskGroup {
    return &TaskGroup{
        Tasks: tasks,
        ready: make(chan struct{}),
    }
}

func NewWorkerPool(workers int) *WorkerPool {
	pool := &WorkerPool{
		Tasks:    make(chan Task, 100), // TODO: make buffer size configurable
		Workers:  workers,
		stopChan: make(chan struct{}),
	}
	pool.start()
	return pool
}
