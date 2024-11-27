package endpoints

// TODO: uncomment when endpoints has seen some more development
/*
// Confirm that the EndpointRequest struct implements the Task interface.
func TestEndpointRequestImplementsTask(t *testing.T) {
	var _ scheduler.Task = &EndpointRequest{}

	// If the above line compiles, the test passes, so no further action is required.
}

// Confirm that the EndpointRequestHTTP struct implements the Task interface.
func TestEndpointRequestHTTPImplementsTask(t *testing.T) {
	var _ scheduler.Task = &EndpointRequestHTTP{}

	// If the above line compiles, the test passes, so no further action is required.
}

// Confirm that the EndpointRequest struct implements the Task interface.
func TestNetworkCheckImplementsTask(t *testing.T) {
	var _ scheduler.Task = &NetworkCheck{}

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
			URL:     *serverURL,
		},
		Secure: false,
	}

	// Execute request
	result := erh.Execute()
	if result.Error != nil {
		t.Errorf("Expected nil error, got %v", result.Error)
	}

	// Check result
	assert.True(t, result.Success)
	// Assert that the types are correct
	assert.NotNil(t, result.Data["duration"])
	assert.NotNil(t, result.Data["response"])
	var timeDuration time.Duration
	var mapStrIntf map[string]interface{}
	assert.IsType(t, result.Data["duration"], timeDuration)
	assert.IsType(t, result.Data["response"], mapStrIntf)
	// Assert that the duration is greater than 0
	assert.Less(t, time.Duration(0), result.Data["duration"])
	// Assert that the response is not empty and contains the expected key
	assert.NotEmpty(t, result.Data["response"])
	assert.Contains(t, result.Data["response"], "status")
	assert.Equal(t, result.Data["response"].(map[string]interface{})["status"], "OK")
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
*/
