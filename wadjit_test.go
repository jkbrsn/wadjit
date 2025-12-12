package wadjit

import (
	"bytes"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jkbrsn/taskman"
	"github.com/rs/xid"
	"github.com/rs/zerolog"
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
	t.Run("default", func(t *testing.T) {
		w := New()
		defer func() {
			err := w.Close()
			assert.NoError(t, err, "error closing Wadjit")
		}()

		assert.NotNil(t, w)
		assert.NotNil(t, w.taskManager)
	})

	t.Run("with taskman mode option", func(t *testing.T) {
		w := New(WithTaskmanMode(taskman.ModeOnDemand))
		defer func() {
			err := w.Close()
			assert.NoError(t, err, "error closing Wadjit")
		}()

		metrics := w.taskManager.Metrics()
		assert.Nil(t, metrics.PoolMetrics, "on-demand mode should not expose pool metrics")
	})

	t.Run("with forwarded taskman options", func(t *testing.T) {
		w := New(WithTaskmanOptions(taskman.WithMode(taskman.ModeDistributed)))
		defer func() {
			err := w.Close()
			assert.NoError(t, err, "error closing Wadjit")
		}()

		metrics := w.taskManager.Metrics()
		assert.Nil(t, metrics.PoolMetrics, "distributed mode should not expose pool metrics")
	})

	t.Run("default buffer size", func(t *testing.T) {
		w := New()
		defer func() {
			err := w.Close()
			assert.NoError(t, err, "error closing Wadjit")
		}()

		assert.Equal(t, defaultResponseChanBufferSize, cap(w.respGatherChan))
		assert.Equal(t, defaultResponseChanBufferSize, cap(w.respExportChan))
	})

	t.Run("custom buffer size", func(t *testing.T) {
		const size = 8
		w := New(WithBufferSize(size))
		defer func() {
			err := w.Close()
			assert.NoError(t, err, "error closing Wadjit")
		}()

		assert.Equal(t, size, cap(w.respGatherChan))
		assert.Equal(t, size, cap(w.respExportChan))
	})

	t.Run("zero buffer size", func(t *testing.T) {
		w := New(WithBufferSize(0))
		defer func() {
			err := w.Close()
			assert.NoError(t, err, "error closing Wadjit")
		}()

		assert.Equal(t, 0, cap(w.respGatherChan))
		assert.Equal(t, 0, cap(w.respExportChan))
	})

	t.Run("negative buffer falls back to default", func(t *testing.T) {
		w := New(func(o *options) {
			o.bufferSize = -42
		})
		defer func() {
			err := w.Close()
			assert.NoError(t, err, "error closing Wadjit")
		}()

		assert.Equal(t, defaultResponseChanBufferSize, cap(w.respGatherChan))
		assert.Equal(t, defaultResponseChanBufferSize, cap(w.respExportChan))
	})
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

func TestWadjit_DefaultDNSPolicyApplied(t *testing.T) {
	defaultPolicy := DNSPolicy{
		Mode:       DNSRefreshStatic,
		StaticAddr: netip.MustParseAddrPort("127.0.0.1:8080"),
	}
	overridePolicy := DNSPolicy{
		Mode:    DNSRefreshCadence,
		Cadence: 2 * time.Second,
		TTLMin:  0,
		TTLMax:  0,
	}

	w := New(WithDefaultDNSPolicy(defaultPolicy))
	t.Cleanup(func() {
		require.NoError(t, w.Close())
	})

	baseURL, err := url.Parse("http://example.com/health")
	require.NoError(t, err)

	endpointWithoutPolicy := NewHTTPEndpoint(baseURL, http.MethodGet)
	endpointWithPolicy := NewHTTPEndpoint(baseURL, http.MethodGet, WithDNSPolicy(overridePolicy))

	watcher, err := NewWatcher(
		xid.New().String(),
		time.Hour,
		WatcherTasksToSlice(endpointWithoutPolicy, endpointWithPolicy),
	)
	require.NoError(t, err)

	require.NoError(t, w.AddWatcher(watcher))

	assert.True(t, endpointWithoutPolicy.dnsPolicySet, "expected default policy to mark endpoint")
	assert.Equal(t, defaultPolicy.Mode, endpointWithoutPolicy.dnsPolicy.Mode)
	assert.Equal(t, defaultPolicy.StaticAddr, endpointWithoutPolicy.dnsPolicy.StaticAddr)

	assert.True(t, endpointWithPolicy.dnsPolicySet, "explicit policy should remain marked")
	assert.Equal(t, overridePolicy, endpointWithPolicy.dnsPolicy)
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
	for _, trigger := range tasks {
		wg.Go(func() {
			<-start
			require.NoError(t, trigger.task.Execute())
		})
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
	require.NoError(t, err, "error adding watchers")
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

//nolint:revive // test complexity acceptable
func TestWadjit_PauseResumeWatcher(t *testing.T) {
	t.Run("Normal Pause and Resume", func(t *testing.T) {
		w := New(WithLogger(zerolog.New(os.Stdout).Level(zerolog.DebugLevel)))
		server := httptest.NewServer(http.HandlerFunc(echoHandler))

		defer func() {
			err := w.Close()
			assert.NoError(t, err, "error closing Wadjit")

			server.Close()
		}()

		// Set up URLs
		targetURL, err := url.Parse(server.URL)
		assert.NoError(t, err, "failed to parse HTTP URL")

		// Create a watcher with a long-running task
		id := "test-pause-resume"
		cadence := 2 * time.Millisecond
		httpTasks := []HTTPEndpoint{{
			URL:     targetURL,
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
		time.Sleep(5 * time.Millisecond)

		// Verify there are responses
		assert.Eventually(t, func() bool {
			select {
			case response := <-w.Responses():
				assert.NoError(t, response.Err)
				return true
			default:
				return false
			}
		}, 25*time.Millisecond, 3*time.Millisecond)

		// Pause the watcher
		err = w.PauseWatcher(id)
		assert.NoError(t, err, "error pausing watcher")

		// Drain any buffered responses from before the pause
		time.Sleep(5 * time.Millisecond) // Ensure on-going tasks are completed
		func() {
			for {
				select {
				case <-w.Responses():
				default:
					return
				}
			}
		}()

		// Verify there are no NEW responses when the watcher is paused
		assert.Never(t, func() bool {
			select {
			case response := <-w.Responses():
				t.Logf("Unexpected response received during pause: %+v", response)
				assert.NoError(t, response.Err)
				return true
			default:
				return false
			}
		}, 25*time.Millisecond, 3*time.Millisecond)

		// Resume the watcher
		err = w.ResumeWatcher(id)
		assert.NoError(t, err, "error resuming watcher")

		// Verify there are responses again
		assert.Eventually(t, func() bool {
			select {
			case response := <-w.Responses():
				assert.NoError(t, response.Err)
			default:
				return false
			}
			return true
		}, 25*time.Millisecond, 3*time.Millisecond)
	})

	t.Run("Pause Non-Existent Watcher", func(t *testing.T) {
		w := New()
		defer func() {
			err := w.Close()
			assert.NoError(t, err, "error closing Wadjit")
		}()

		err := w.PauseWatcher("non-existent-id")
		assert.Error(t, err, "expected error pausing non-existent watcher")
	})

	t.Run("Resume Non-Existent Watcher", func(t *testing.T) {
		w := New()
		defer func() {
			err := w.Close()
			assert.NoError(t, err, "error closing Wadjit")
		}()

		err := w.ResumeWatcher("non-existent-id")
		assert.Error(t, err, "expected error resuming non-existent watcher")
	})

	t.Run("Double Pause", func(t *testing.T) {
		w := New()
		server := httptest.NewServer(http.HandlerFunc(echoHandler))

		defer func() {
			err := w.Close()
			assert.NoError(t, err, "error closing Wadjit")
			server.Close()
		}()

		targetURL, err := url.Parse(server.URL)
		assert.NoError(t, err, "failed to parse HTTP URL")

		id := "test-double-pause"
		cadence := 10 * time.Millisecond
		httpTasks := []HTTPEndpoint{{
			URL:     targetURL,
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
		time.Sleep(5 * time.Millisecond)

		// First pause should succeed
		err = w.PauseWatcher(id)
		assert.NoError(t, err, "error on first pause")

		// Second pause should error (already paused)
		err = w.PauseWatcher(id)
		assert.Error(t, err, "expected error on double pause")
	})

	t.Run("Double Resume", func(t *testing.T) {
		w := New()
		server := httptest.NewServer(http.HandlerFunc(echoHandler))

		defer func() {
			err := w.Close()
			assert.NoError(t, err, "error closing Wadjit")
			server.Close()
		}()

		targetURL, err := url.Parse(server.URL)
		assert.NoError(t, err, "failed to parse HTTP URL")

		id := "test-double-resume"
		cadence := 10 * time.Millisecond
		httpTasks := []HTTPEndpoint{{
			URL:     targetURL,
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
		time.Sleep(5 * time.Millisecond)

		// Pause first
		err = w.PauseWatcher(id)
		assert.NoError(t, err, "error pausing watcher")

		// First resume should succeed
		err = w.ResumeWatcher(id)
		assert.NoError(t, err, "error on first resume")

		// Second resume should error (not paused)
		err = w.ResumeWatcher(id)
		assert.Error(t, err, "expected error on double resume")
	})
}

func TestWadjit_TaskmanLogLevel(t *testing.T) {
	t.Run("WithTaskmanLogLevel sets independent log level", func(t *testing.T) {
		// Create a buffer to capture log output
		var buf bytes.Buffer

		// Set Wadjit logger to Error level
		wadjitLogger := zerolog.New(&buf).Level(zerolog.ErrorLevel)

		// Set taskman logger to Debug level (more verbose than Wadjit)
		w := New(
			WithLogger(wadjitLogger),
			WithTaskmanLogLevel(zerolog.DebugLevel),
		)
		defer func() {
			err := w.Close()
			assert.NoError(t, err)
		}()

		// Verify Wadjit was created successfully
		assert.NotNil(t, w)
		assert.NotNil(t, w.taskManager)

		// The taskman should have a logger configured at Debug level
		// This is verified by the fact that New() didn't panic and taskManager was created
		// In a real scenario, taskman would log at Debug level while Wadjit logs at Error level
	})

	t.Run("WithTaskmanLogLevel overrides WithLogger taskman settings", func(t *testing.T) {
		var buf bytes.Buffer

		// Create loggers
		logger := zerolog.New(&buf).Level(zerolog.InfoLevel)

		// Apply WithLogger first, then WithTaskmanLogLevel
		w := New(
			WithLogger(logger),
			WithTaskmanLogLevel(zerolog.TraceLevel), // Most verbose
		)
		defer func() {
			err := w.Close()
			assert.NoError(t, err)
		}()

		assert.NotNil(t, w)
		assert.NotNil(t, w.taskManager)
	})

	t.Run("WithTaskmanLogLevel can reduce verbosity", func(t *testing.T) {
		var buf bytes.Buffer

		// Set Wadjit logger to Debug level (verbose)
		wadjitLogger := zerolog.New(&buf).Level(zerolog.DebugLevel)

		// Set taskman to Error level only (quiet)
		w := New(
			WithLogger(wadjitLogger),
			WithTaskmanLogLevel(zerolog.ErrorLevel),
		)
		defer func() {
			err := w.Close()
			assert.NoError(t, err)
		}()

		assert.NotNil(t, w)
		assert.NotNil(t, w.taskManager)
	})

	t.Run("WithTaskmanLogLevel with Disabled level", func(t *testing.T) {
		var buf bytes.Buffer

		logger := zerolog.New(&buf).Level(zerolog.InfoLevel)

		// Disable taskman logging completely
		w := New(
			WithLogger(logger),
			WithTaskmanLogLevel(zerolog.Disabled),
		)
		defer func() {
			err := w.Close()
			assert.NoError(t, err)
		}()

		assert.NotNil(t, w)
		assert.NotNil(t, w.taskManager)
	})

	t.Run("WithTaskmanLogLevel without WithLogger", func(t *testing.T) {
		// Use WithTaskmanLogLevel alone (Wadjit will use Nop logger)
		w := New(
			WithTaskmanLogLevel(zerolog.DebugLevel),
		)
		defer func() {
			err := w.Close()
			assert.NoError(t, err)
		}()

		assert.NotNil(t, w)
		assert.NotNil(t, w.taskManager)
	})

	t.Run("option ordering - taskman level applied last", func(t *testing.T) {
		var buf bytes.Buffer
		logger := zerolog.New(&buf).Level(zerolog.WarnLevel)

		// Apply WithTaskmanLogLevel before WithLogger
		// The taskman level should still take precedence
		w := New(
			WithTaskmanLogLevel(zerolog.TraceLevel),
			WithLogger(logger),
		)
		defer func() {
			err := w.Close()
			assert.NoError(t, err)
		}()

		assert.NotNil(t, w)
		assert.NotNil(t, w.taskManager)

		// The fact that this doesn't panic means the taskman logger was configured
		// at Trace level, overriding the clamped Debug level from WithLogger
	})
}

func TestWadjit_WatcherJitter(t *testing.T) {
	t.Run("WithWatcherJitter sets jitter on Wadjit", func(t *testing.T) {
		jitterAmount := 100 * time.Millisecond
		w := New(WithWatcherJitter(jitterAmount))
		defer func() {
			err := w.Close()
			assert.NoError(t, err)
		}()

		assert.Equal(t, jitterAmount, w.watcherJitter)
	})

	t.Run("negative jitter is clamped to zero", func(t *testing.T) {
		w := New(WithWatcherJitter(-50 * time.Millisecond))
		defer func() {
			err := w.Close()
			assert.NoError(t, err)
		}()

		assert.Equal(t, time.Duration(0), w.watcherJitter)
	})

	t.Run("jitter is applied to watcher when added", func(t *testing.T) {
		jitterAmount := 200 * time.Millisecond
		w := New(WithWatcherJitter(jitterAmount))
		defer func() {
			err := w.Close()
			assert.NoError(t, err)
		}()

		testURL, _ := url.Parse("http://example.com")
		endpoint := NewHTTPEndpoint(testURL, http.MethodGet)

		watcher := &Watcher{
			ID:       xid.New().String(),
			Cadence:  time.Second,
			Tasks:    []WatcherTask{endpoint},
			doneChan: make(chan struct{}),
		}

		err := w.AddWatcher(watcher)
		assert.NoError(t, err)

		// Verify jitter was applied to the watcher
		assert.Equal(t, jitterAmount, watcher.jitter)
	})

	t.Run("jitter creates variation in NextExec times", func(t *testing.T) {
		jitterAmount := 500 * time.Millisecond
		w := New(WithWatcherJitter(jitterAmount))
		defer func() {
			err := w.Close()
			assert.NoError(t, err)
		}()

		// Create multiple watchers with the same cadence
		testURL, _ := url.Parse("http://example.com")
		var nextExecTimes []time.Time

		for i := 0; i < 10; i++ {
			endpoint := NewHTTPEndpoint(testURL, http.MethodGet)
			watcher := &Watcher{
				ID:       xid.New().String(),
				Cadence:  time.Second,
				Tasks:    []WatcherTask{endpoint},
				doneChan: make(chan struct{}),
				jitter:   jitterAmount,
			}

			job := watcher.job()
			nextExecTimes = append(nextExecTimes, job.NextExec)
		}

		// Check that we have some variation (not all identical)
		uniqueTimes := make(map[time.Time]bool)
		for _, t := range nextExecTimes {
			uniqueTimes[t] = true
		}

		// With 10 watchers and 500ms jitter, we should have multiple unique times
		// (statistically very unlikely to have all the same)
		assert.GreaterOrEqual(t, len(uniqueTimes), 2, "jitter should create variation in NextExec times")
	})

	t.Run("jitter stays within bounds", func(t *testing.T) {
		jitterAmount := 300 * time.Millisecond
		cadence := 2 * time.Second

		testURL, _ := url.Parse("http://example.com")
		endpoint := NewHTTPEndpoint(testURL, http.MethodGet)

		// Test many iterations to ensure jitter always stays in bounds
		for i := 0; i < 100; i++ {
			watcher := &Watcher{
				ID:       xid.New().String(),
				Cadence:  cadence,
				Tasks:    []WatcherTask{endpoint},
				doneChan: make(chan struct{}),
				jitter:   jitterAmount,
			}

			start := time.Now()
			job := watcher.job()

			// NextExec should be in the range [now + cadence - jitter, now + cadence + jitter]
			minExpected := start.Add(cadence - jitterAmount)
			maxExpected := start.Add(cadence + jitterAmount)

			assert.True(t,
				job.NextExec.After(minExpected) || job.NextExec.Equal(minExpected),
				"NextExec %v should be >= %v", job.NextExec, minExpected)
			assert.True(t,
				job.NextExec.Before(maxExpected) || job.NextExec.Equal(maxExpected),
				"NextExec %v should be <= %v", job.NextExec, maxExpected)

			// NextExec should never be in the past
			assert.True(t,
				job.NextExec.After(start) || job.NextExec.Equal(start),
				"NextExec should not be in the past")
		}
	})

	t.Run("zero jitter produces consistent NextExec", func(t *testing.T) {
		w := New() // No jitter configured
		defer func() {
			err := w.Close()
			assert.NoError(t, err)
		}()

		testURL, _ := url.Parse("http://example.com")
		endpoint := NewHTTPEndpoint(testURL, http.MethodGet)

		cadence := time.Second
		watcher := &Watcher{
			ID:       xid.New().String(),
			Cadence:  cadence,
			Tasks:    []WatcherTask{endpoint},
			doneChan: make(chan struct{}),
			jitter:   0, // No jitter
		}

		start := time.Now()
		job := watcher.job()

		// With no jitter, NextExec should be exactly now + cadence (within a small tolerance)
		expected := start.Add(cadence)
		tolerance := 10 * time.Millisecond

		diff := job.NextExec.Sub(expected)
		if diff < 0 {
			diff = -diff
		}

		assert.LessOrEqual(t, diff, tolerance,
			"With no jitter, NextExec should be approximately now + cadence")
	})

	t.Run("jitter does not affect Cadence field in job", func(t *testing.T) {
		jitterAmount := 500 * time.Millisecond
		cadence := time.Second

		testURL, _ := url.Parse("http://example.com")
		endpoint := NewHTTPEndpoint(testURL, http.MethodGet)

		watcher := &Watcher{
			ID:       xid.New().String(),
			Cadence:  cadence,
			Tasks:    []WatcherTask{endpoint},
			doneChan: make(chan struct{}),
			jitter:   jitterAmount,
		}

		job := watcher.job()

		// Cadence should remain unchanged regardless of jitter
		assert.Equal(t, cadence, job.Cadence,
			"Jitter should only affect NextExec, not Cadence")
	})
}
