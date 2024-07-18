package scheduling

import (
	"sync"
	"time"

	"github.com/rs/zerolog/log"
)

// TODO: finish implementation of groups
type Result struct {
	Error      error
	Latency    time.Duration
	StatusCode int
}

// Task is an interface for tasks that can be executed.
type Task interface {
	Execute() (Result, error)

	Cadence() time.Duration
}

// DefaultTask is a basic task implementation used ONLY in tests and development.
type DefaultTask struct {
	// TODO: consider creating a unique TaskID type
	ID       string // Unique identifier, e.g., endpoint URL
	// TODO: also consider a GroupID for grouping tasks
	cadence  time.Duration
}

// TODO: finish implementation of groups
type TaskGroup struct {
    Tasks    []Task
    ready    chan struct{} // Channel to signal readiness for execution
    waitGroup sync.WaitGroup
}

// Cadence returns the cadence of the DefaultTask.
func (dt DefaultTask) Cadence() time.Duration {
	return dt.cadence
}

// Execute executes the DefaultTask.
func (dt DefaultTask) Execute() (Result, error) {
	log.Debug().Msgf("Executing task %s", dt.ID)
	// Placeholder: Implement task execution logic
	return Result{}, nil
}

// NewDefaultTask creates and returns a new DefaultTask.
func NewDefaultTask(id string, cadence time.Duration) *DefaultTask {
	return &DefaultTask{
		ID:      id,
		cadence: cadence,
	}
}