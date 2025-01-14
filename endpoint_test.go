package wadjit

import (
	"net/url"
	"testing"

	"github.com/rs/xid"
	"github.com/stretchr/testify/assert"
)

func TestNewEndpoint(t *testing.T) {
	url, _ := url.Parse("http://localhost:8080")
	id := xid.New()
	e := Endpoint{
		ID:  id,
		URL: *url,
	}
	assert.NotNil(t, e)
	assert.NotNil(t, e.URL)
}
