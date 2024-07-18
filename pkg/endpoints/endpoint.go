package endpoints

import (
	"time"

	"github.com/jakobilobi/wadjit/pkg/schedule"
)

// EndpointRequest is a struct representing a request used to monitor an endpoint.
type EndpointRequest struct {
	ID      string
	cadence time.Duration
}

// Cadence returns the cadence of the EndpointRequest.
func (e EndpointRequest) Cadence() time.Duration {
	return e.cadence
}

// Execute executes the EndpointRequest.
func (e EndpointRequest) Execute() schedule.Result {
	// TODO: Implement task execution logic
	return schedule.Result{}
}
