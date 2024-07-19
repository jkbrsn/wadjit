package endpoints

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/jakobilobi/wadjit/pkg/schedule"
	"github.com/rs/zerolog/log"
	"github.com/stretchr/testify/assert"
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

// Confirm that the EndpointRequest struct implements the Task interface.
func TestNetworkCheckImplementsTask(t *testing.T) {
	var _ schedule.Task = &NetworkCheck{}

	// If the above line compiles, the test passes, so no further action is required.
}

func TestNetworkCheckCadence(t *testing.T) {
	nc := NetworkCheck{cadence: 5}
	if nc.Cadence() != 5 {
		t.Errorf("Expected cadence 5, got %d", nc.Cadence())
	}
}

func TestNetworkCheckExecute(t *testing.T) {
	nc := NetworkCheck{}
	result := nc.Execute()
	if result.Error != nil {
		t.Errorf("Expected nil error, got %v", result.Error)
	}
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
	// Start test server
	server := testServer(t)
	defer server.Close()

	// Build request
	serverURL, _ := url.Parse(server.URL)
	log.Debug().Msgf("Server URL: %v", serverURL)
	erh := EndpointRequestHTTP{
		EndpointRequest: EndpointRequest{
			cadence: 5,
			ID:      "test",
			URL:     *serverURL},
		Secure: false,
	}

	// Execute request
	result := erh.Execute()
	if result.Error != nil {
		t.Errorf("Expected nil error, got %v", result.Error)
	}

	// Check result
	assert.Equal(t, result.StatusCode, 200)
	assert.Equal(t, result.Data["status"], "OK")
	assert.True(t, result.Latency > time.Duration(0))
}

// HELPERS

func testServer(t *testing.T) *httptest.Server {
	// Start a local HTTP server
	server := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		// Test request parameters
		assert.Equal(t, req.URL.String(), "/")
		// Send response to be tested
		jsonString := `{"status": "OK"}`
		rw.Write([]byte(jsonString))
	}))
	return server
}
