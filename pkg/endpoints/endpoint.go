package endpoints

import (
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
	// TODO: Implement task execution logic
	return schedule.Result{}
}

func NewEndpointRequest(id string, cadence time.Duration, url url.URL) *EndpointRequest {
	return &EndpointRequest{
		cadence: cadence,
		ID:      id,
		URL:     url,
	}
}

func NewEndpointRequestHTTP(id string, cadence time.Duration, url url.URL) *EndpointRequestHTTP {
	return &EndpointRequestHTTP{
		EndpointRequest: EndpointRequest{
			cadence: cadence,
			ID:      id,
			URL:     url,
		},
		Secure: true,
	}
}
