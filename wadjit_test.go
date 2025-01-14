package wadjit

import (
	"net/url"
	"sync"
	"testing"

	"github.com/rs/xid"
	"github.com/stretchr/testify/assert"
)

func syncMapLen(m *sync.Map) int {
	var length int
	m.Range(func(key, value interface{}) bool {
		length++
		return true
	})
	return length
}

func TestNewWadjit(t *testing.T) {
	w := New()
	defer w.Stop()

	assert.NotNil(t, w)
	assert.NotNil(t, w.taskManager)
}

func TestAddEndpoint(t *testing.T) {
	w := New()
	defer w.Stop()

	url, _ := url.Parse("http://localhost:8080")
	id := xid.New()
	e := Endpoint{
		ID:  id,
		URL: *url,
	}
	w.AddEndpoint(e)

	assert.Equal(t, 1, syncMapLen(&w.endpoints))
	loaded, _ := w.endpoints.Load(id)
	assert.NotNil(t, loaded)
	loaded = loaded.(Endpoint)
	assert.Equal(t, e, loaded)
}
