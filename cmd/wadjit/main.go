package main

import (
	"log"
	"time"

	"github.com/jakobilobi/wadjit/pkg/scheduling"
)

func main() {
	// TODO: evaluate and adjust buffer sizes
    taskChannel := make(chan scheduling.Task, 2000) // Buffered channel to hold tasks
    resultChannel := make(chan scheduling.Result, 100) // Buffered channel to hold results

	// TODO: add flags for configuring the worker pool size and refresh rate etc.
	// TODO: add endpoint DB client and fetch tasks from the DB, give handle to the manager

	//workerPool := scheduling.NewWorkerPool(10)
	scheduler := scheduling.NewScheduler(taskChannel)


	// TODO: add waitgroup to ensure all goroutines are stopped before exiting, and to gracefully handle shutdown
    go scheduler.Start()
    //go workerPool.Start()

	// Add tasks with varying cadences
	scheduler.AddTask(&scheduling.DefaultTask{ID: "http://example.com/1", Cadence: 10 * time.Second}, 10 * time.Second)
	scheduler.AddTask(&scheduling.DefaultTask{ID: "http://example.com/2", Cadence: 20 * time.Second}, 20 * time.Second)
	scheduler.AddTask(&scheduling.DefaultTask{ID: "http://example.com/3", Cadence: 30 * time.Second}, 30 * time.Second)

	// Process results and write to the external database
	for result := range resultChannel {
		if result.Error != nil {
			log.Printf("Task failed: %v", result.Error)
		} else {
			log.Printf("Task succeeded")
		}
	}
}
