package wadjit

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jkbrsn/taskman"
	"github.com/rs/xid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type taskTrigger struct {
	task taskman.Task
}

func newTaskTrigger(t *testing.T, wt WatcherTask) (taskTrigger, string) {
	t.Helper()

	var taskID string
	switch typed := wt.(type) {
	case *HTTPEndpoint:
		taskID = typed.ID
	case *WSEndpoint:
		taskID = typed.ID
	case *MockWatcherTask:
		taskID = typed.ID
	default:
		t.Fatalf("unsupported watcher task type %T", wt)
	}

	return taskTrigger{task: wt.Task()}, taskID
}

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
	testCases := []struct {
		name string
		test func(t *testing.T, w *Wadjit)
	}{
		{
			name: "add watcher",
			test: func(t *testing.T, w *Wadjit) {
				id := xid.New().String()
				watcher, err := getHTTPWatcher(id, 1*time.Second, []byte("test payload"))
				require.NoError(t, err, "error creating watcher")

				require.NoError(t, w.AddWatcher(watcher))
				time.Sleep(3 * time.Millisecond)

				require.Equal(t, 1, syncMapLen(&w.watchers))
				loaded, ok := w.watchers.Load(id)
				require.True(t, ok)
				require.NotNil(t, loaded)
				loadedWatcher, castOK := loaded.(*Watcher)
				require.True(t, castOK, "expected loaded value to be *Watcher")
				assert.Equal(t, watcher, loadedWatcher)
			},
		},
		{
			name: "add duplicate",
			test: func(t *testing.T, w *Wadjit) {
				id := xid.New().String()
				watcher, err := getHTTPWatcher(id, 1*time.Second, []byte("test payload"))
				require.NoError(t, err, "error creating watcher")

				require.NoError(t, w.AddWatcher(watcher))
				time.Sleep(3 * time.Millisecond)
				assert.Equal(t, 1, syncMapLen(&w.watchers))

				err = w.AddWatcher(watcher)
				require.Error(t, err, "expected error adding duplicate watcher")

				newWatcher, err := getHTTPWatcher(id, 2*time.Second, []byte("another payload"))
				require.NoError(t, err, "error creating new watcher")
				err = w.AddWatcher(newWatcher)
				require.Error(t, err, "expected error adding duplicate watcher")
			},
		},
		{
			name: "add watchers",
			test: func(t *testing.T, w *Wadjit) {
				targetCount := 10
				watchers := make([]*Watcher, 0, targetCount)
				for i := 0; i < targetCount; i++ {
					id := xid.New().String()
					watcher, err := getHTTPWatcher(
						id,
						1*time.Second,
						fmt.Appendf(nil, "test payload %d", i),
					)
					require.NoError(t, err, "error creating watcher")
					watchers = append(watchers, watcher)
				}

				require.NoError(t, w.AddWatchers(watchers...))
				time.Sleep(10 * time.Millisecond)

				assert.Equal(t, targetCount, syncMapLen(&w.watchers))
			},
		},
		{
			name: "nil watcher",
			test: func(t *testing.T, w *Wadjit) {
				err := w.AddWatcher(nil)
				assert.Error(t, err, "expected error adding nil watcher")
			},
		},
		{
			name: "invalid watcher",
			test: func(t *testing.T, w *Wadjit) {
				id := xid.New().String()
				watcher, err := getHTTPWatcher(id, 1*time.Second, []byte("test payload"))
				require.NoError(t, err, "error creating watcher")
				watcher.Cadence = 0

				err = w.AddWatcher(watcher)
				assert.Error(t, err, "expected error removing invalid watcher")
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			w := New()
			defer func() {
				err := w.Close()
				assert.NoError(t, err, "error closing Wadjit")
			}()

			tc.test(t, w)
		})
	}
}

func TestWadjit_ConcurrentWatchers(t *testing.T) {
	w := New()
	defer func() {
		err := w.Close()
		assert.NoError(t, err, "error closing Wadjit")
	}()

	server := httptest.NewServer(http.HandlerFunc(echoHandler))
	defer server.Close()

	httpBaseURL, err := url.Parse(server.URL)
	require.NoError(t, err, "failed to parse HTTP URL")

	wsURL := "ws" + server.URL[4:] + "/ws"
	wsParsedURL, err := url.Parse(wsURL)
	require.NoError(t, err, "failed to parse WS URL")

	watchers := []*Watcher{}

	httpWatcher1, err := NewWatcher(
		xid.New().String(),
		25*time.Millisecond,
		WatcherTasksToSlice(
			NewHTTPEndpoint(
				httpBaseURL.ResolveReference(&url.URL{Path: "/one"}),
				http.MethodPost,
				WithHeader(http.Header{"Content-Type": []string{"application/json"}}),
				WithPayload([]byte(`{"case":"one"}`)),
			),
		),
	)
	require.NoError(t, err, "error creating http watcher 1")
	watchers = append(watchers, httpWatcher1)

	httpWatcher2, err := NewWatcher(
		xid.New().String(),
		30*time.Millisecond,
		WatcherTasksToSlice(
			NewHTTPEndpoint(
				httpBaseURL.ResolveReference(&url.URL{Path: "/two"}),
				http.MethodGet,
				WithHeader(http.Header{"Accept": []string{"text/plain"}}),
			),
		),
	)
	require.NoError(t, err, "error creating http watcher 2")
	watchers = append(watchers, httpWatcher2)

	wsWatcher, err := NewWatcher(
		xid.New().String(),
		35*time.Millisecond,
		WatcherTasksToSlice(
			NewWSEndpoint(
				wsParsedURL,
				http.Header{},
				OneHitText,
				[]byte(`{"case":"ws"}`),
				"",
			),
		),
	)
	require.NoError(t, err, "error creating ws watcher")
	watchers = append(watchers, wsWatcher)

	require.NoError(t, w.AddWatchers(watchers...))

	type expectation struct {
		watcherID string
	}

	expectedTasks := map[string]expectation{}
	tasks := []taskTrigger{}

	for _, watcher := range watchers {
		for _, wt := range watcher.Tasks {
			trigger, taskID := newTaskTrigger(t, wt)
			expectedTasks[taskID] = expectation{watcherID: watcher.ID}
			tasks = append(tasks, trigger)
		}
	}

	start := make(chan struct{})
	var wg sync.WaitGroup
	for _, task := range tasks {
		wg.Add(1)
		go func(tr taskTrigger) {
			defer wg.Done()
			<-start
			require.NoError(t, tr.task.Execute())
		}(task)
	}

	close(start)
	wg.Wait()

	received := map[string]bool{}
	deadline := time.After(500 * time.Millisecond)
	for len(received) < len(expectedTasks) {
		select {
		case resp := <-w.Responses():
			require.NotNil(t, resp)
			require.NoError(t, resp.Err)
			exp, ok := expectedTasks[resp.TaskID]
			if !ok {
				continue
			}
			assert.Equal(t, exp.watcherID, resp.WatcherID)
			data, err := resp.Data()
			require.NoError(t, err)
			assert.NotEmpty(t, data)
			received[resp.TaskID] = true
		case <-deadline:
			t.Fatal("timeout waiting for responses")
		}
	}
}

func TestWadjit_ErrorResponsesForwarded(t *testing.T) {
	w := New()
	defer func() {
		err := w.Close()
		assert.NoError(t, err, "error closing Wadjit")
	}()

	errTask := &MockWatcherTask{
		URL:             &url.URL{Scheme: "http", Host: "example.com", Path: "/err"},
		ID:              "err-task",
		ErrTaskResponse: errors.New("mock failure"),
	}
	okTask := &MockWatcherTask{
		URL:     &url.URL{Scheme: "http", Host: "example.com", Path: "/ok"},
		ID:      "ok-task",
		Payload: []byte("ok"),
	}

	watcher, err := NewWatcher(
		xid.New().String(),
		20*time.Millisecond,
		[]WatcherTask{errTask, okTask},
	)
	require.NoError(t, err, "error creating watcher")

	require.NoError(t, w.AddWatcher(watcher))

	errExecute := errTask.Task().Execute()
	require.Error(t, errExecute)

	select {
	case resp := <-w.Responses():
		require.Equal(t, errTask.ID, resp.TaskID)
		require.Equal(t, watcher.ID, resp.WatcherID)
		require.Error(t, resp.Err)
		assert.Contains(t, resp.Err.Error(), "mock failure")
		assert.Nil(t, resp.Payload)
	case <-time.After(200 * time.Millisecond):
		t.Fatal("expected error response")
	}

	okExecute := okTask.Task().Execute()
	require.NoError(t, okExecute)

	select {
	case resp := <-w.Responses():
		require.Equal(t, okTask.ID, resp.TaskID)
		require.Equal(t, watcher.ID, resp.WatcherID)
		require.NoError(t, resp.Err)
		data, err := resp.Data()
		require.NoError(t, err)
		assert.Equal(t, []byte("ok"), data)
	case <-time.After(200 * time.Millisecond):
		t.Fatal("expected success response after error")
	}
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
	httpTasks := []HTTPEndpoint{{URL: &url.URL{
		Scheme: "http",
		Host:   "localhost:8080",
	}, Payload: payload}}
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
		watcher, err := getHTTPWatcher(id, 1*time.Second, fmt.Appendf(nil, "test payload %d", i))
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
	testURL, err := url.Parse(server.URL)
	assert.NoError(t, err, "failed to parse HTTP URL")

	// Create first watcher
	id1 := xid.New().String()
	tasks := append([]WatcherTask{}, &HTTPEndpoint{
		URL:     testURL,
		Method:  http.MethodPost,
		Payload: []byte("first")},
	)
	watcher1, err := NewWatcher(
		id1,
		5*time.Millisecond,
		tasks,
	)
	assert.NoError(t, err, "error creating watcher 1")
	// Create second watcher
	id2 := xid.New().String()
	tasks = append([]WatcherTask{}, &HTTPEndpoint{
		URL:     testURL,
		Method:  http.MethodPost,
		Payload: []byte("second")},
	)
	watcher2, err := NewWatcher(
		id2,
		15*time.Millisecond,
		tasks,
	)
	assert.NoError(t, err, "error creating watcher 2")
	// Create third watcher
	id3 := xid.New().String()
	wsURL := "ws" + server.URL[4:] + "/ws"
	wsParsedURL, err := url.Parse(wsURL)
	assert.NoError(t, err, "failed to parse URL")
	tasks = append([]WatcherTask{}, &WSEndpoint{URL: wsParsedURL, Payload: []byte("third")})
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
	tasks = append([]WatcherTask{}, &HTTPEndpoint{
		URL:     testURL,
		Method:  http.MethodPost,
		Payload: []byte("first"),
	})
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

	// Drain responses to prevent blocking
	go func() {
		for resp := range w.Responses() {
			_ = resp // Drain response to prevent channel blocking
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
	assert.True(t, idsMap["watcher-1"] && idsMap["watcher-2"],
		"expected IDs 'watcher-1' and 'watcher-2', got %v", idsAfterAdd)

	// Remove a watcher and check IDs after removal
	require.NoError(t, w.RemoveWatcher("watcher-1"))

	idsAfterRemove := w.WatcherIDs()
	assert.Equal(t, 1, len(idsAfterRemove), "expected 1 watcher ID after removal")
	assert.Equal(t, "watcher-2", idsAfterRemove[0],
		"expected remaining ID 'watcher-2', got %v", idsAfterRemove[0])
}
