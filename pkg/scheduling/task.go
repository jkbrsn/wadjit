package scheduling

import (
	"sync"
	"time"
)

type Result struct {
	Error      error
	Latency    time.Duration
	StatusCode int
}

// Task is an interface for tasks that can be executed.
type Task interface {
	Execute() (Result, error)
}

// DefaultTask is a basic task implementation used ONLY in tests and development.
type DefaultTask struct {
	// TODO: consider creating a unique TaskID type
	ID       string // Unique identifier, e.g., endpoint URL
	// TODO: also consider a GroupID for grouping tasks
	Cadence  time.Duration
}

type TaskGroup struct {
    Tasks    []Task
    ready    chan struct{} // Channel to signal readiness for execution
    waitGroup sync.WaitGroup
}

// Execute executes the DefaultTask.
func (dt DefaultTask) Execute() (Result, error) {
	// Placeholder: Implement task execution logic
	return Result{}, nil
}