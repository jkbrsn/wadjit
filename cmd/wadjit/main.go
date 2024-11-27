package main

import (
	"time"

	"github.com/jakobilobi/wadjit/pkg/scheduler"
	"github.com/rs/zerolog/log"
)

// ExampleTask is an example task implementation.
type ExampleTask struct {
	// TODO: consider creating a unique TaskID type
	ID string // Unique identifier for the task
	// TODO: also consider a GroupID for grouping tasks
	cadence time.Duration
}

// Cadence returns the cadence of the ExampleTask.
func (dt ExampleTask) Cadence() time.Duration {
	return dt.cadence
}

// Execute executes the ExampleTask.
func (dt ExampleTask) Execute() scheduler.Result {
	log.Info().Msgf("Executing task %s", dt.ID)
	// Placeholder: Implement task execution logic
	return scheduler.Result{}
}

// NewExampleTask creates and returns a new ExampleTask.
func NewExampleTask(id string, cadence time.Duration) *ExampleTask {
	return &ExampleTask{
		ID:      id,
		cadence: cadence,
	}
}

func main() {
	// TODO: move this example implementation into the scheduler package
	taskScheduler := scheduler.NewScheduler(10, 8, 8)
	results := taskScheduler.Results()

	taskScheduler.AddTask(NewExampleTask("CADENCE 2s", 2*time.Second), "")
	taskScheduler.AddTask(NewExampleTask("CADENCE 3s", 3*time.Second), "")
	taskScheduler.AddTask(NewExampleTask("CADENCE 5s", 5*time.Second), "")

	// Process results
	// TODO: implement some actual processing logic
	for result := range results {
		if result.Error != nil {
			log.Printf("Task failed: %v", result.Error)
		} else {
			log.Printf("Task succeeded")
		}
	}
}
