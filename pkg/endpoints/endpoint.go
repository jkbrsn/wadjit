package endpoints

import (
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/jakobilobi/wadjit/pkg/scheduler"
	"github.com/rs/zerolog/log"
)

// TODO: remove
type NetworkCheck struct {
	cadence   time.Duration
	Name      string
	Endpoints []EndpointRequest
}

// TODO: bring back as struct?
// EndpointRequest is an interface for requests used to monitor an endpoint.
type EndpointRequest interface {
	Execute() map[string]interface{}
	Name() string
}

// TODO: adapt to group functionality (or perhaps that's built into Task)
type EndpointRequestHTTP struct {
	EndpointRequest

	ID     string
	URL    url.URL
	Secure bool // Whether to use HTTPS
}

// Cadence returns the cadence of the NetworkCheck.
func (nc NetworkCheck) Cadence() time.Duration {
	log.Trace().Msgf("NetworkCheck cadence: %v", nc.cadence)
	return nc.cadence
}

// Execute executes the NetworkCheck.
func (nc NetworkCheck) Execute() scheduler.Result {
	log.Trace().Msgf("NetworkCheck executing: %v", nc)

	resultData := make(map[string]interface{})
	for _, endpoint := range nc.Endpoints {
		log.Trace().Msgf("Executing endpoint: %v", endpoint)
		data := endpoint.Execute()
		log.Trace().Msgf("Endpoint data: %v", data)
		resultData[endpoint.Name()] = data
	}
	return scheduler.Result{
		Data:    resultData,
		Error:   nil,
		Success: true,
	}
}

func (erh EndpointRequestHTTP) Execute() map[string]interface{} {
	log.Trace().Msgf("EndpointRequestHTTP executing: %v", erh)

	req, err := http.NewRequest(http.MethodGet, erh.URL.String(), nil)
	if err != nil {
		log.Error().Err(err).Caller().Str("ID", erh.ID).Msg("Error creating request")
		return map[string]interface{}{"error": err}
	}

	client := &http.Client{
		Timeout: time.Second * 5,
	}

	start := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		log.Error().Err(err).Caller().Str("ID", erh.ID).Msgf("Error making request")
		return map[string]interface{}{"error": err}
	}
	defer resp.Body.Close()
	duration := time.Since(start)

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		log.Error().Err(err).Caller().Str("ID", erh.ID).Msg("Error reading response body")
		return map[string]interface{}{"error": err}
	}
	log.Trace().Str("ID", erh.ID).Msgf("Response body: %s", body)

	var response map[string]interface{}
	err = json.Unmarshal(body, &response)
	if err != nil {
		log.Error().Err(err).Caller().Str("ID", erh.ID).Msg("Error unmarshalling response body")
		return map[string]interface{}{"error": err}
	}

	data := map[string]interface{}{
		"duration": duration,
		"response": response,
		"url":      erh.URL.String(),
	}
	return data
}

func (erh EndpointRequestHTTP) Name() string {
	return erh.ID
}

func NewEndpointRequestHTTP(id string, url url.URL) *EndpointRequestHTTP {
	log.Debug().Msgf("Creating new EndpointRequestHTTP with ID %s, and URL %v", id, url)
	return &EndpointRequestHTTP{
		ID:     id,
		URL:    url,
		Secure: true,
	}
}
