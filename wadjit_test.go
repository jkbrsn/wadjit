package wadjit

import (
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/rs/xid"
	"github.com/stretchr/testify/assert"
)

// TODO: create a MockHTTPWatcher struct that implements the Watcher interface
// TODO: create a MockWSWatcher struct that implements the Watcher interface
// TODO: set up a small test server that can be used to test the Wadjit

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
	defer w.Close()

	assert.NotNil(t, w)
	assert.NotNil(t, w.taskManager)
}

func TestAddWatcher(t *testing.T) {
	w := New()
	defer w.Close()

	id := xid.New()
	watcher := &Watcher{
		id:      id,
		cadence: 1 * time.Second,
		http:    []Endpoint{{URL: &url.URL{Scheme: "http", Host: "localhost:8080"}}},
	}
	w.AddWatcher(watcher)
	time.Sleep(5 * time.Millisecond) // wait for watcher to be added

	assert.Equal(t, 1, syncMapLen(&w.watchers))
	loaded, _ := w.watchers.Load(id)
	assert.NotNil(t, loaded)
	loaded = loaded.(*Watcher)
	assert.Equal(t, watcher, loaded)
}

// TODO: test adding/removing multiple watchers

func TestRemoveWatcher(t *testing.T) {
	w := New()
	defer w.Close()

	id := xid.New()
	watcher := &Watcher{
		id:      id,
		cadence: 1 * time.Second,
		http:    []Endpoint{{URL: &url.URL{Scheme: "http", Host: "localhost:8080"}}},
	}
	w.AddWatcher(watcher)
	time.Sleep(5 * time.Millisecond) // wait for watcher to be added

	assert.Equal(t, 1, syncMapLen(&w.watchers))

	err := w.RemoveWatcher(id)
	assert.NoError(t, err)
	assert.Equal(t, 0, syncMapLen(&w.watchers))

	loaded, ok := w.watchers.Load(id)
	assert.Nil(t, loaded)
	assert.False(t, ok)
}

// TODO: test watcher initialization
// TODO: test watcher execution for HTTP
// TODO: test watcher execution for WS
