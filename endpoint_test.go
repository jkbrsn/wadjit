package wadjit

import (
	"net/url"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestNewEndpoint(t *testing.T) {
	url, _ := url.Parse("http://localhost:8080")
	e := Endpoint{
		URL: url,
	}
	assert.NotNil(t, e)
	assert.NotNil(t, e.URL)
}
