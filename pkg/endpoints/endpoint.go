package endpoints

import (
	"time"

	"github.com/jakobilobi/wadjit/pkg/schedule"
)

// Endpoint is a struct representing an endpoint to be monitored.
type Endpoint struct {
	ID      string
	cadence time.Duration
}

// Cadence returns the cadence of the Endpoint.
func (e Endpoint) Cadence() time.Duration {
	return e.cadence
}

// Execute executes the Endpoint.
func (e Endpoint) Execute() schedule.Result {
	// TODO: Implement task execution logic
	return schedule.Result{}
}
