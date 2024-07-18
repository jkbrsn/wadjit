package main

import "github.com/jakobilobi/wadjit/pkg/scheduling"

func main() {
	// TODO: add flags for configuring the worker pool size and refresh rate etc.
	// TODO: add endpoint DB client and fetch tasks from the DB, give handle to the manager

	workerPool := scheduling.NewWorkerPool(10)
	manager := scheduling.NewCadenceManager(workerPool)

	// TODO: add waitgroup to ensure all goroutines are stopped before exiting, and to gracefully handle shutdown
	go manager.RefreshTasksPeriodically()
	go manager.StartScheduling()
}
