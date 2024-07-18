package main

import (
	"log"
	"time"

	"github.com/jakobilobi/wadjit/pkg/schedule"
)

func main() {
	// TODO: evaluate and adjust buffer sizes
	taskChannel := make(chan schedule.Task, 1000)    // Buffered channel to hold tasks
	resultChannel := make(chan schedule.Result, 100) // Buffered channel to hold results

	// TODO: add flags for configuring the worker pool size and refresh rate etc.

	workerPool := schedule.NewWorkerPool(resultChannel, taskChannel, 10)
	scheduler := schedule.NewScheduler(taskChannel)

	// TODO: add waitgroup/shutdown procedure to ensure all goroutines are stopped before exiting, and to gracefully handle shutdown
	go scheduler.Start()
	go workerPool.Start()

	// TODO: add endpoint DB client and fetch tasks from the DB, add these as tasks in scheduler
	// DEV: add tasks with varying cadences
	scheduler.AddTask(schedule.NewDefaultTask("http://example.com/2", 2*time.Second))
	scheduler.AddTask(schedule.NewDefaultTask("http://example.com/3", 3*time.Second))
	scheduler.AddTask(schedule.NewDefaultTask("http://example.com/5", 5*time.Second))

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
