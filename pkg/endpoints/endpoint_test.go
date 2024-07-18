package endpoints

import (
	"testing"

	"github.com/jakobilobi/wadjit/pkg/schedule"
)

// Confirm that the Endpoint struct implements the Task interface.
func TestEndpointImplementsTask(t *testing.T) {
	var _ schedule.Task = &Endpoint{}

	// If the above line compiles, the test passes, so no further action is required.
}
