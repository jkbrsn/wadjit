package wadjit

import (
	"net/url"
	"sync"
	"testing"
	"time"

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

func TestAddWatcher(t *testing.T) {
	w := New()
	defer w.Stop()

	id := xid.New()
	watcher := &HTTPWatcher{
		id:        id,
		cadence:   1 * time.Second,
		endpoints: []Endpoint{{URL: &url.URL{Scheme: "http", Host: "localhost:8080"}}},
	}
	w.AddWatcher(watcher)
	time.Sleep(5 * time.Millisecond) // wait for watcher to be added

	assert.Equal(t, 1, syncMapLen(&w.watchers))
	loaded, _ := w.watchers.Load(id)
	assert.NotNil(t, loaded)
	loaded = loaded.(Watcher)
	assert.Equal(t, watcher, loaded)
}
