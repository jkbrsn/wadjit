package wadjit

import (
	"net/http"
	"net/url"
)

// Endpoint represents an endpoint.
type Endpoint struct {
	Header http.Header
	URL    *url.URL
}
