package main

import (
	"log"
	"time"

	"github.com/jakobilobi/wadjit/pkg/scheduler"
)

func main() {
	// TODO: evaluate and adjust buffer sizes
	taskChannel := make(chan scheduler.Task, 1000)    // Buffered channel to hold tasks
	resultChannel := make(chan scheduler.Result, 100) // Buffered channel to hold results

	// TODO: add flags for configuring the worker pool size and refresh rate etc.

	// TODO: should the scheduler set up the worker pool inside itself?
	workerPool := scheduler.NewWorkerPool(resultChannel, taskChannel, 10)
	taskScheduler := scheduler.NewScheduler(taskChannel)

	// TODO: add waitgroup/shutdown procedure to ensure all goroutines are stopped before exiting, and to gracefully handle shutdown
	// TODO: these doesn't need to run in separate goroutines any more
	go taskScheduler.Start()
	go workerPool.Start()

	// TODO: add endpoint DB client and fetch tasks from the DB, add these as tasks in scheduler
	// DEV: add tasks with varying cadences
	taskScheduler.AddTask(scheduler.NewDefaultTask("http://example.com/2", 2*time.Second), "")
	taskScheduler.AddTask(scheduler.NewDefaultTask("http://example.com/3", 3*time.Second), "")
	taskScheduler.AddTask(scheduler.NewDefaultTask("http://example.com/5", 5*time.Second), "")

	// Process results and write to the external database
	// TODO: implement actual processing logic
	for result := range resultChannel {
		if result.Error != nil {
			log.Printf("Task failed: %v", result.Error)
		} else {
			log.Printf("Task succeeded")
		}
	}
}
