package scheduler

import (
	"sync/atomic"

	"github.com/rs/zerolog/log"
)

type WorkerPool struct {
	ResultChannel chan Result
	TaskChannel   chan Task
	WorkerCount   int

	activeWorkers atomic.Int32 // Use atomic operations to manage this counter
}

// worker executes tasks from the task channel.
func (wp *WorkerPool) worker(id int) {
	for task := range wp.TaskChannel {
		log.Trace().Msgf("Worker %d executing task", id)
		wp.activeWorkers.Add(1) // Increment active workers
		result := task.Execute()
		wp.ResultChannel <- result
		wp.activeWorkers.Add(-1) // Decrement active workers
		log.Trace().Msgf("Worker %d finished task", id)
	}
}

// ActiveWorkers returns the number of active workers.
func (wp *WorkerPool) ActiveWorkers() int32 {
	return wp.activeWorkers.Load()
}

// Start starts the worker pool, creating workers according to wp.WorkerCount.
func (wp *WorkerPool) Start() {
	for i := 0; i < wp.WorkerCount; i++ {
		go wp.worker(i)
	}
}

// Stop signals the worker pool to stop processing tasks and exit.
func (wp *WorkerPool) Stop() {
	close(wp.TaskChannel)
}

// NewWorkerPool creates a worker pool.
func NewWorkerPool(resultChannel chan Result, taskChannel chan Task, workerCount int) *WorkerPool {
	pool := &WorkerPool{
		ResultChannel: resultChannel,
		TaskChannel:   taskChannel,
		WorkerCount:   workerCount,
	}
	return pool
}
