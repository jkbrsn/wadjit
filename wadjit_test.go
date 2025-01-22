package wadjit

import (
	"io"
	"net/url"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rs/xid"
	"github.com/stretchr/testify/assert"
)

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
	watcher, err := NewWatcher(id, cadence, payload, tasks)
	assert.NoError(t, err, "error creating watcher")

	// Add the watcher
	err = w.AddWatcher(watcher)
	assert.NoError(t, err, "error adding watcher")
	w.consumeStarted <- struct{}{}   // Unblock Watcher adding (otherwise done by w.ResponseChannel())
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
	watcher, err := NewWatcher(id, cadence, payload, tasks)
	assert.NoError(t, err, "error creating watcher")

	err = w.AddWatcher(watcher)
	assert.NoError(t, err, "error adding watcher")
	w.consumeStarted <- struct{}{}   // Unblock Watcher adding (otherwise done by w.ResponseChannel())
	time.Sleep(5 * time.Millisecond) // Wait for watcher to be added

	assert.Equal(t, 1, syncMapLen(&w.watchers))

	err = w.RemoveWatcher(id)
	assert.NoError(t, err)
	assert.Equal(t, 0, syncMapLen(&w.watchers))

	loaded, ok := w.watchers.Load(id)
	assert.Nil(t, loaded)
	assert.False(t, ok)
}

func TestWadjitLifecycle(t *testing.T) {
	w := New()
	defer w.Close()
	server := echoServer()
	defer server.Close()

	// Set up URLs
	url, err := url.Parse(server.URL)
	assert.NoError(t, err, "failed to parse HTTP URL")

	// Create first watcher
	id1 := xid.New()
	tasks := append([]WatcherTask{}, &HTTPEndpoint{URL: url})
	watcher1, err := NewWatcher(
		id1,
		5*time.Millisecond,
		[]byte("first"),
		tasks,
	)
	assert.NoError(t, err, "error creating watcher 1")
	// Create second watcher
	id2 := xid.New()
	tasks = append([]WatcherTask{}, &HTTPEndpoint{URL: url})
	watcher2, err := NewWatcher(
		id2,
		15*time.Millisecond,
		[]byte("second"),
		tasks,
	)
	assert.NoError(t, err, "error creating watcher 2")

	// Consume responses
	responses := w.ResponsesChannel()
	firstCount := atomic.Int32{}
	secondCount := atomic.Int32{}
	go func() {
		for {
			select {
			case response := <-responses:
				assert.NotNil(t, response.HTTPResponse)
				data, err := io.ReadAll(response.HTTPResponse.Body)
				assert.NoError(t, err)
				if string(data) == "first" {
					firstCount.Add(1)
				} else if string(data) == "second" {
					secondCount.Add(1)
				}
			case <-w.doneChan:
				return
			}
		}
	}()

	// Add watchers
	t.Logf("Adding watcher 1: %s", id1)
	err = w.AddWatcher(watcher1)
	assert.NoError(t, err, "error adding watcher 1")
	t.Logf("Adding watcher 2: %s", id2)
	err = w.AddWatcher(watcher2)
	assert.NoError(t, err, "error adding watcher 2")
	time.Sleep(2 * time.Millisecond) // Wait for watchers to be added
	assert.Equal(t, 2, syncMapLen(&w.watchers))

	// Let the watchers run
	time.Sleep(20 * time.Millisecond)

	// Confirm the first watcher executed more tasks than the second
	assert.Greater(t, firstCount.Load(), secondCount.Load())

	// Remove the first watcher
	err = w.RemoveWatcher(id1)
	assert.NoError(t, err)
	assert.Equal(t, 1, syncMapLen(&w.watchers))

	// Try adding the same Watcher again
	err = w.AddWatcher(watcher1)
	assert.Error(t, err, "expected error adding a closed watcher")

	// TODO: consider testing cloning watcher1, or unsetting the doneChan, to allow re-adding it
}

// TODO: test Wadjit execution for WS
