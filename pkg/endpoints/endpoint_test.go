package endpoints

import (
	"testing"

	"github.com/jakobilobi/wadjit/pkg/schedule"
)

// Confirm that the EndpointRequest struct implements the Task interface.
func TestEndpointRequestImplementsTask(t *testing.T) {
	var _ schedule.Task = &EndpointRequest{}

	// If the above line compiles, the test passes, so no further action is required.
}
