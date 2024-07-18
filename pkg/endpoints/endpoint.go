package endpoints

import (
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/jakobilobi/wadjit/pkg/schedule"
	"github.com/rs/zerolog/log"
)

// EndpointRequest is a struct representing a request used to monitor an endpoint.
type EndpointRequest struct {
	cadence time.Duration
	ID      string
	URL     url.URL
}

type EndpointRequestHTTP struct {
	EndpointRequest

	Secure bool // Whether to use HTTPS
}

// Cadence returns the cadence of the EndpointRequest.
func (er EndpointRequest) Cadence() time.Duration {
	log.Trace().Msgf("EndpointRequest cadence: %v", er.cadence)
	return er.cadence
}

// Execute executes the EndpointRequest.
func (er EndpointRequest) Execute() schedule.Result {
	log.Trace().Msgf("EndpointRequest executing: %v", er)
	// TODO: Implement task execution logic
	return schedule.Result{}
}

func (erh EndpointRequestHTTP) Execute() schedule.Result {
	log.Trace().Msgf("EndpointRequestHTTP executing: %v", erh)

	req, err := http.NewRequest(http.MethodGet, erh.URL.String(), nil)
	if err != nil {
		log.Error().Err(err).Caller().Str("ID", erh.ID).Msg("Error creating request")
		return schedule.Result{Error: err}
	}

	client := &http.Client{
		Timeout: time.Second * 5,
	}

	start := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		log.Error().Err(err).Caller().Str("ID", erh.ID).Msgf("Error making request")
		return schedule.Result{Error: err}
	}
	defer resp.Body.Close()
	duration := time.Since(start)

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		log.Error().Err(err).Caller().Str("ID", erh.ID).Msg("Error reading response body")
		return schedule.Result{Error: err}
	}
	log.Trace().Str("ID", erh.ID).Msgf("Response body: %s", body)

	var response map[string]interface{}
	err = json.Unmarshal(body, &response)
	if err != nil {
		log.Error().Err(err).Caller().Str("ID", erh.ID).Msg("Error unmarshalling response body")
		return schedule.Result{Error: err}
	}

	result := schedule.Result{
		Data:       response,
		Error:      nil,
		Latency:    duration,
		StatusCode: resp.StatusCode,
	}
	return result
}

func NewEndpointRequest(id string, cadence time.Duration, url url.URL) *EndpointRequest {
	log.Debug().Msgf("Creating new EndpointRequest with ID %s, cadence %v, and URL %v", id, cadence, url)
	return &EndpointRequest{
		cadence: cadence,
		ID:      id,
		URL:     url,
	}
}

func NewEndpointRequestHTTP(id string, cadence time.Duration, url url.URL) *EndpointRequestHTTP {
	log.Debug().Msgf("Creating new EndpointRequestHTTP with ID %s, cadence %v, and URL %v", id, cadence, url)
	return &EndpointRequestHTTP{
		EndpointRequest: EndpointRequest{
			cadence: cadence,
			ID:      id,
			URL:     url,
		},
		Secure: true,
	}
}
