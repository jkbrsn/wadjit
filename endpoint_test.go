package wadjit

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestNewEndpoint(t *testing.T) {
	e := Endpoint{URL: "http://localhost:8080"}
	assert.NotNil(t, e)
	assert.NotNil(t, e.URL)
}
