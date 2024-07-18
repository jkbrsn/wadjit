package schedule

import (
	"time"

	"github.com/rs/zerolog/log"
)

// TODO: finish implementation of groups

// Result represents the result of a task execution.
// TODO: interfacify this too?
type Result struct {
	Error      error
	Latency    time.Duration
	StatusCode int
}

// Task is an interface for tasks that can be executed.
type Task interface {
	Execute() Result

	Cadence() time.Duration
}

// DefaultTask is a example task implementation, used in tests and development.
type DefaultTask struct {
	// TODO: consider creating a unique TaskID type
	ID string // Unique identifier, e.g., endpoint URL
	// TODO: also consider a GroupID for grouping tasks
	cadence time.Duration
}

// Cadence returns the cadence of the DefaultTask.
func (dt DefaultTask) Cadence() time.Duration {
	return dt.cadence
}

// Execute executes the DefaultTask.
func (dt DefaultTask) Execute() Result {
	log.Debug().Msgf("Executing task %s", dt.ID)
	// Placeholder: Implement task execution logic
	return Result{}
}

// NewDefaultTask creates and returns a new DefaultTask.
func NewDefaultTask(id string, cadence time.Duration) *DefaultTask {
	return &DefaultTask{
		ID:      id,
		cadence: cadence,
	}
}
