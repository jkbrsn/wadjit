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

// Confirm that the EndpointRequestHTTP struct implements the Task interface.
func TestEndpointRequestHTTPImplementsTask(t *testing.T) {
	var _ schedule.Task = &EndpointRequestHTTP{}

	// If the above line compiles, the test passes, so no further action is required.
}

func TestEndpointRequestCadence(t *testing.T) {
	er := EndpointRequest{cadence: 5}
	if er.Cadence() != 5 {
		t.Errorf("Expected cadence 5, got %d", er.Cadence())
	}
}

func TestEndpointRequestHTTPCadence(t *testing.T) {
	er := EndpointRequestHTTP{
		EndpointRequest: EndpointRequest{cadence: 5},
	}
	if er.Cadence() != 5 {
		t.Errorf("Expected cadence 5, got %d", er.Cadence())
	}
}

func TestEndpointRequestExecute(t *testing.T) {
	er := EndpointRequest{}
	result := er.Execute()
	if result.Error != nil {
		t.Errorf("Expected nil error, got %v", result.Error)
	}
}

func TestEndpointRequestHTTPExecute(t *testing.T) {
	er := EndpointRequestHTTP{}
	result := er.Execute()
	if result.Error != nil {
		t.Errorf("Expected nil error, got %v", result.Error)
	}
}
