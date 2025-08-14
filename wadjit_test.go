package wadjit

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rs/xid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewWadjit(t *testing.T) {
	w := New()
	defer func() {
		err := w.Close()
		assert.NoError(t, err, "error closing Wadjit")
	}()

	assert.NotNil(t, w)
	assert.NotNil(t, w.taskManager)
}

func TestWadjit_AddWatcher(t *testing.T) {
	w := New()
	defer func() {
		err := w.Close()
		assert.NoError(t, err, "error closing Wadjit")
	}()

	// Create a watcher
	id := xid.New().String()
	watcher, err := getHTTPWatcher(id, 1*time.Second, []byte("test payload"))
	assert.NoError(t, err, "error creating watcher")

	t.Run("add watcher", func(t *testing.T) {
		// Add the watcher
		err = w.AddWatcher(watcher)
		assert.NoError(t, err, "error adding watcher")
		// Give watcher time to start up
		time.Sleep(3 * time.Millisecond)

		// Check that the watcher was added correctly
		assert.Equal(t, 1, syncMapLen(&w.watchers))
		loaded, _ := w.watchers.Load(id)
		assert.NotNil(t, loaded)
		loaded = loaded.(*Watcher)
		assert.Equal(t, watcher, loaded)
	})

	t.Run("add duplicate", func(t *testing.T) {
		// Add the same watcher again
		err = w.AddWatcher(watcher)
		assert.Error(t, err, "expected error adding duplicate watcher")
		// Give watcher time to start up
		time.Sleep(3 * time.Millisecond)

		// Add new watcher with the same ID
		newWatcher, err := getHTTPWatcher(id, 2*time.Second, []byte("another payload"))
		assert.NoError(t, err, "error creating new watcher")
		err = w.AddWatcher(newWatcher)
		assert.Error(t, err, "expected error adding duplicate watcher")
	})

	t.Run("add watchers", func(t *testing.T) {
		// Add multiple watchers
		watchers := []*Watcher{}
		for i := range 10 {
			id := xid.New().String()
			watcher, err := getHTTPWatcher(id, 1*time.Second, []byte(fmt.Sprintf("test payload %d", i)))
			assert.NoError(t, err, "error creating watcher")
			watchers = append(watchers, watcher)
		}
		err = w.AddWatchers(watchers...)
		assert.NoError(t, err, "error adding watchers")
		// Give watchers time to start up
		time.Sleep(10 * time.Millisecond)

		// Check that the watchers were added correctly
		assert.Equal(t, 11, syncMapLen(&w.watchers))
	})

	t.Run("nil watcher", func(t *testing.T) {
		err := w.AddWatcher(nil)
		assert.Error(t, err, "expected error adding nil watcher")
	})

	t.Run("invalid watcher", func(t *testing.T) {
		// Create a watcher with invalid configuration
		id := xid.New().String()
		watcher, err := getHTTPWatcher(id, 1*time.Second, []byte("test payload"))
		assert.NoError(t, err, "error creating watcher")
		watcher.Cadence = 0

		err = w.AddWatcher(watcher)
		assert.Error(t, err, "expected error removing invalid watcher")
	})
}

func TestWadjit_RemoveWatcher(t *testing.T) {
	w := New()
	defer func() {
		err := w.Close()
		assert.NoError(t, err, "error closing Wadjit")
	}()

	// Create a watcher
	id := xid.New().String()
	cadence := 1 * time.Second
	payload := []byte("test payload")
	httpTasks := []HTTPEndpoint{{URL: &url.URL{Scheme: "http", Host: "localhost:8080"}, Payload: payload}}
	var tasks []WatcherTask
	for _, task := range httpTasks {
		tasks = append(tasks, &task)
	}
	watcher, err := NewWatcher(id, cadence, tasks)
	assert.NoError(t, err, "error creating watcher")

	err = w.AddWatcher(watcher)
	assert.NoError(t, err, "error adding watcher")
	// Give watcher time to start up
	time.Sleep(3 * time.Millisecond)

	assert.Equal(t, 1, syncMapLen(&w.watchers))

	err = w.RemoveWatcher(id)
	assert.NoError(t, err)
	assert.Equal(t, 0, syncMapLen(&w.watchers))

	loaded, ok := w.watchers.Load(id)
	assert.Nil(t, loaded)
	assert.False(t, ok)
}

func TestWadjit_Clear(t *testing.T) {
	w := New()
	defer func() {
		err := w.Close()
		assert.NoError(t, err, "error closing Wadjit")
	}()

	// Add multiple watchers
	watchers := []*Watcher{}
	for i := range 10 {
		id := xid.New().String()
		watcher, err := getHTTPWatcher(id, 1*time.Second, []byte(fmt.Sprintf("test payload %d", i)))
		assert.NoError(t, err, "error creating watcher")
		watchers = append(watchers, watcher)
	}
	err := w.AddWatchers(watchers...)
	assert.NoError(t, err, "error adding watchers")
	// Give watchers time to start up
	time.Sleep(5 * time.Millisecond)

	// Check that the watchers were added correctly
	assert.Equal(t, 10, syncMapLen(&w.watchers))

	// Clear the Wadjit
	err = w.Clear()
	assert.NoError(t, err, "error clearing Wadjit")

	// Check that all watchers were cleared
	assert.Equal(t, 0, syncMapLen(&w.watchers))
}

func TestWadjit_Close(t *testing.T) {
	t.Run("Normal Close", func(t *testing.T) {
		w := New()

		// Create a watcher with a long-running task
		id := xid.New().String()
		cadence := 100 * time.Millisecond
		httpTasks := []HTTPEndpoint{{
			URL:     &url.URL{Scheme: "http", Host: "localhost:8080"},
			Payload: []byte("test payload"),
		}}
		var tasks []WatcherTask
		for _, task := range httpTasks {
			tasks = append(tasks, &task)
		}
		watcher, err := NewWatcher(id, cadence, tasks)
		assert.NoError(t, err, "error creating watcher")

		err = w.AddWatcher(watcher)
		assert.NoError(t, err, "error adding watcher")
		// Give it some time to start running
		time.Sleep(3 * time.Millisecond)

		// Close the Wadjit
		err = w.Close()
		assert.NoError(t, err, "error closing Wadjit")

		_, open := <-w.respGatherChan
		assert.False(t, open, "respGatherChan should be closed")

		_, open = <-w.respExportChan
		assert.False(t, open, "respExportChan should be closed")

		// Verify no watchers remain
		assert.Equal(t, 0, syncMapLen(&w.watchers), "all watchers should be removed")

		// Verify double close doesn't panic
		err = w.Close()
		assert.NoError(t, err, "error on second close")
	})

	t.Run("With Pending Responses", func(t *testing.T) {
		w := New()
		server := httptest.NewServer(http.HandlerFunc(echoHandler))
		defer server.Close()

		// Set up URLs
		url, err := url.Parse(server.URL)
		assert.NoError(t, err, "failed to parse HTTP URL")

		// Create a watcher with a very short cadence
		id := xid.New().String()
		cadence := 1 * time.Millisecond
		httpTasks := []HTTPEndpoint{{
			URL:     url,
			Payload: []byte("test payload"),
		}}
		var tasks []WatcherTask
		for _, task := range httpTasks {
			tasks = append(tasks, &task)
		}
		watcher, err := NewWatcher(id, cadence, tasks)
		assert.NoError(t, err, "error creating watcher")

		err = w.AddWatcher(watcher)
		assert.NoError(t, err, "error adding watcher")

		// Give time to generate some responses
		time.Sleep(10 * time.Millisecond)

		// Close the Wadjit
		err = w.Close()
		assert.NoError(t, err, "error closing Wadjit")

		// Verify we can still read responses from the channel
		// (they should be drained before closing)
		responseCount := 0
		responses := w.Responses()
		for response := range responses {
			responseCount++
			assert.NoError(t, response.Err)
		}
		assert.Greater(t, responseCount, 0, "should have received responses before close")
	})
}

func TestWadjit_Lifecycle(t *testing.T) {
	w := New()
	server := httptest.NewServer(http.HandlerFunc(echoHandler))
	defer func() {
		// Make sure the Wadjit is closed before the server closes
		err := w.Close()
		assert.NoError(t, err, "error closing Wadjit")
		server.Close()
	}()

	// Set up URLs
	url, err := url.Parse(server.URL)
	assert.NoError(t, err, "failed to parse HTTP URL")

	// Create first watcher
	id1 := xid.New().String()
	tasks := append([]WatcherTask{}, &HTTPEndpoint{URL: url, Method: http.MethodPost, Payload: []byte("first")})
	watcher1, err := NewWatcher(
		id1,
		5*time.Millisecond,
		tasks,
	)
	assert.NoError(t, err, "error creating watcher 1")
	// Create second watcher
	id2 := xid.New().String()
	tasks = append([]WatcherTask{}, &HTTPEndpoint{URL: url, Method: http.MethodPost, Payload: []byte("second")})
	watcher2, err := NewWatcher(
		id2,
		15*time.Millisecond,
		tasks,
	)
	assert.NoError(t, err, "error creating watcher 2")
	// Create third watcher
	id3 := xid.New().String()
	wsURL := "ws" + server.URL[4:] + "/ws"
	url, err = url.Parse(wsURL)
	assert.NoError(t, err, "failed to parse URL")
	tasks = append([]WatcherTask{}, &WSEndpoint{URL: url, Payload: []byte("third")})
	watcher3, err := NewWatcher(
		id3,
		10*time.Millisecond,
		tasks,
	)
	assert.NoError(t, err, "error creating watcher 3")

	// Consume responses
	firstCount := atomic.Int32{}
	secondCount := atomic.Int32{}
	thirdCount := atomic.Int32{}
	go func() {
		for {
			select {
			case response := <-w.Responses():
				assert.NoError(t, response.Err)
				require.NotNil(t, response.Payload)
				data, err := response.Payload.Data()
				assert.NoError(t, err)
				if string(data) == "first" {
					firstCount.Add(1)
				} else if string(data) == "second" {
					secondCount.Add(1)
				} else if string(data) == "third" {
					thirdCount.Add(1)
				}
			case <-w.ctx.Done():
				return
			}
		}
	}()

	// Add watchers
	err = w.AddWatchers(watcher1, watcher2, watcher3)
	assert.NoError(t, err, "error adding watchers")
	time.Sleep(5 * time.Millisecond) // Wait for watchers to be added
	assert.Equal(t, 3, syncMapLen(&w.watchers))

	// Let the watchers run
	time.Sleep(25 * time.Millisecond)

	// Confirm the first watcher executed more tasks than the second
	assert.Greater(t, firstCount.Load(), secondCount.Load())
	assert.Greater(t, firstCount.Load(), thirdCount.Load())
	assert.GreaterOrEqual(t, thirdCount.Load(), secondCount.Load())

	// Remove the first watcher
	err = w.RemoveWatcher(id1)
	assert.NoError(t, err)
	assert.Equal(t, 2, syncMapLen(&w.watchers))

	// Try adding the same Watcher again
	err = w.AddWatcher(watcher1)
	assert.Error(t, err, "expected error adding a closed watcher")

	// Try adding a new Watcher, but with the ID of the removed Watcher
	tasks = append([]WatcherTask{}, &HTTPEndpoint{URL: url, Method: http.MethodPost, Payload: []byte("first")})
	watcher4, err := NewWatcher(
		id1,
		5*time.Millisecond,
		tasks,
	)
	assert.NoError(t, err, "error creating watcher 4")
	err = w.AddWatcher(watcher4)
	assert.NoError(t, err)
}

func TestWadjit_WatcherIDs(t *testing.T) {
	w := New()
	defer func() {
		err := w.Close()
		assert.NoError(t, err, "error closing Wadjit")
	}()

	// Initial state: no watchers
	initialIDs := w.WatcherIDs()
	assert.Zero(t, len(initialIDs), "expected 0 watcher IDs initially")

	// Add watchers
	url1, _ := url.Parse("http://example.com/1")
	task1 := NewHTTPEndpoint(
		url1,
		http.MethodGet,
		WithID(""),
	)
	w1, err := NewWatcher("watcher-1", 1*time.Second, WatcherTasksToSlice(task1))
	require.NoError(t, err, "error creating watcher 1")

	url2, _ := url.Parse("http://example.com/2")
	task2 := NewHTTPEndpoint(
		url2,
		http.MethodGet,
		WithID(""),
	)
	w2, err := NewWatcher("watcher-2", 1*time.Second, WatcherTasksToSlice(task2))
	require.NoError(t, err, "error creating watcher 2")

	require.NoError(t, w.AddWatchers(w1, w2))

	// Drain responses
	go func() {
		for range w.Responses() {
		}
	}()

	// Allow time for watchers to be set up
	time.Sleep(10 * time.Millisecond)

	// Check IDs after adding
	idsAfterAdd := w.WatcherIDs()
	assert.Equal(t, 2, len(idsAfterAdd), "expected 2 watcher IDs after adding")
	// Use a map for easier checking regardless of order
	idsMap := make(map[string]bool)
	for _, id := range idsAfterAdd {
		idsMap[id] = true
	}
	assert.True(t, idsMap["watcher-1"] && idsMap["watcher-2"], "expected IDs 'watcher-1' and 'watcher-2', got %v", idsAfterAdd)

	// Remove a watcher and check IDs after removal
	require.NoError(t, w.RemoveWatcher("watcher-1"))

	idsAfterRemove := w.WatcherIDs()
	assert.Equal(t, 1, len(idsAfterRemove), "expected 1 watcher ID after removal")
	assert.Equal(t, "watcher-2", idsAfterRemove[0], "expected remaining ID 'watcher-2', got %v", idsAfterRemove[0])
}
