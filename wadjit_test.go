package wadjit

import (
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/rs/xid"
	"github.com/stretchr/testify/assert"
)

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

	// Create a watcher
	id := xid.New()
	cadence := 1 * time.Second
	payload := []byte("test payload")
	httpTasks := []HTTPEndpoint{{URL: &url.URL{Scheme: "http", Host: "localhost:8080"}}}
	var tasks []WatcherTask
	for _, task := range httpTasks {
		tasks = append(tasks, &task)
	}
	respChannel := make(chan WatcherResponse)
	watcher, err := NewWatcher(id, cadence, payload, tasks, respChannel)
	assert.NoError(t, err, "error creating watcher")

	// Add the watcher
	err = w.AddWatcher(watcher)
	assert.NoError(t, err, "error adding watcher")
	time.Sleep(5 * time.Millisecond) // wait for watcher to be added

	// Check that the watcher was added correctly
	assert.Equal(t, 1, syncMapLen(&w.watchers))
	loaded, _ := w.watchers.Load(id)
	assert.NotNil(t, loaded)
	loaded = loaded.(*Watcher)
	assert.Equal(t, watcher, loaded)
}

func TestRemoveWatcher(t *testing.T) {
	w := New()
	defer w.Close()

	// Create a watcher
	id := xid.New()
	cadence := 1 * time.Second
	payload := []byte("test payload")
	httpTasks := []HTTPEndpoint{{URL: &url.URL{Scheme: "http", Host: "localhost:8080"}}}
	var tasks []WatcherTask
	for _, task := range httpTasks {
		tasks = append(tasks, &task)
	}
	respChannel := make(chan WatcherResponse)
	watcher, err := NewWatcher(id, cadence, payload, tasks, respChannel)
	assert.NoError(t, err, "error creating watcher")

	err = w.AddWatcher(watcher)
	assert.NoError(t, err, "error adding watcher")
	time.Sleep(5 * time.Millisecond) // Wait for watcher to be added

	assert.Equal(t, 1, syncMapLen(&w.watchers))

	err = w.RemoveWatcher(id)
	assert.NoError(t, err)
	assert.Equal(t, 0, syncMapLen(&w.watchers))

	loaded, ok := w.watchers.Load(id)
	assert.Nil(t, loaded)
	assert.False(t, ok)
}

// TODO: test adding/removing multiple watchers
// TODO: test Wadjit execution for HTTP
// TODO: test Wadjit execution for WS
