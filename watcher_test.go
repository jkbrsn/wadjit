package wadjit

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/jkbrsn/taskman"
	"github.com/rs/xid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type stubWatcherTask struct {
	id         string
	closeErr   error
	initErr    error
	sawNilResp bool
}

func (s *stubWatcherTask) Close() error { return s.closeErr }

func (s *stubWatcherTask) Initialize(_ string, respChan chan<- WatcherResponse) error {
	if respChan == nil {
		s.sawNilResp = true
	}
	return s.initErr
}

func (s *stubWatcherTask) Task() taskman.Task { return stubTask{} }

func (s *stubWatcherTask) Validate() error { return nil }

type stubTask struct{}

func (stubTask) Execute() error { return nil }

func TestWatcherInitialization(t *testing.T) {
	id := xid.New().String()
	cadence := 1 * time.Second
	payload := []byte("test payload")
	httpTasks := []HTTPEndpoint{{
		URL:     &url.URL{Scheme: "http", Host: "localhost:8080"},
		Payload: payload,
	}}
	var tasks []WatcherTask
	for _, task := range httpTasks {
		tasks = append(tasks, &task)
	}
	watcher, err := NewWatcher(id, cadence, tasks)
	assert.NoError(t, err)

	assert.Equal(t, id, watcher.ID)
	assert.Equal(t, cadence, watcher.Cadence)
	assert.NotNil(t, watcher.doneChan)
	assert.NotNil(t, watcher.Tasks)
}

func TestWatcherStart(t *testing.T) {
	id := xid.New().String()
	cadence := 1 * time.Second
	payload := []byte("test payload")
	responseChan := make(chan WatcherResponse)

	watcher := &Watcher{
		ID:      id,
		Cadence: cadence,
		Tasks: []WatcherTask{
			&HTTPEndpoint{
				URL:     &url.URL{Scheme: "http", Host: "localhost:8080"},
				Header:  make(http.Header),
				Payload: payload,
			},
		},
	}

	err := watcher.start(responseChan)
	assert.NoError(t, err)
	assert.NotNil(t, watcher.Tasks)

	// These are not nil, even though uninitialized, since Start initializes them if found nil
	assert.NotNil(t, watcher.doneChan)
}

func TestWatcherCloseAggregatesErrors(t *testing.T) {
	errA := errors.New("close A")
	errB := errors.New("close B")

	watcher := &Watcher{
		ID:      "watcher-close",
		Cadence: 10 * time.Millisecond,
		Tasks: []WatcherTask{
			&stubWatcherTask{id: "a", closeErr: errA},
			&stubWatcherTask{id: "b", closeErr: errB},
		},
		doneChan: make(chan struct{}),
	}

	err := watcher.close()
	require.Error(t, err)
	assert.True(t, errors.Is(err, errA))
	assert.True(t, errors.Is(err, errB))

	select {
	case <-watcher.doneChan:
	default:
		t.Fatal("doneChan should be closed")
	}
}

func TestWatcherStartNilResponseChan(t *testing.T) {
	initErr := errors.New("init failed")
	task := &stubWatcherTask{id: "stub", initErr: initErr}

	watcher := &Watcher{
		ID:      "watcher-start",
		Cadence: 15 * time.Millisecond,
		Tasks:   []WatcherTask{task},
	}

	err := watcher.start(nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "response channel is nil")
	assert.True(t, errors.Is(err, initErr))
	assert.NotNil(t, watcher.doneChan)
	assert.True(t, task.sawNilResp)
}

func TestWatcherExecution(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(echoHandler))
	defer server.Close()

	// Set up URLs
	httpURL, err := url.Parse(server.URL)
	assert.NoError(t, err, "failed to parse HTTP URL")
	wsURL, err := url.Parse("ws" + server.URL[4:] + "/ws")
	assert.NoError(t, err, "failed to parse WS URL")
	header := make(http.Header)

	// Set up watcher
	id := xid.New().String()
	cadence := 1 * time.Second
	payload := []byte("test payload")
	var tasks []WatcherTask
	tasks = append(tasks, &HTTPEndpoint{
		URL: httpURL, Method: http.MethodPost, Header: header, Payload: payload, ID: "an-id"})
	tasks = append(tasks, &WSEndpoint{
		URL: wsURL, Header: header, Payload: payload, ID: "another-id"})
	watcher, err := NewWatcher(id, cadence, tasks)
	assert.NoError(t, err)

	// Start the watcher and execute the tasks
	watcherResponses := make(chan WatcherResponse, 2)
	err = watcher.start(watcherResponses)
	assert.NoError(t, err)
	for _, task := range watcher.Tasks {
		assert.NoError(t, task.Task().Execute())
	}

	// Listen for responses on the watcherResponses channel
	for range watcher.Tasks {
		response := <-watcherResponses
		assert.NotNil(t, response)
		assert.Equal(t, id, response.WatcherID)
		assert.NotNil(t, response.URL)
		assert.Nil(t, response.Err)
		assert.NotNil(t, response.Payload)
		responsePayload, err := response.Payload.Data()
		assert.NoError(t, err)
		assert.Equal(t, payload, responsePayload)
		switch response.URL.Scheme {
		case "http":
			_, ok := response.Payload.(*HTTPTaskResponse)
			assert.True(t, ok, "response.Payload is not of type HTTPTaskResponse")
			assert.Equal(t, "an-id", response.TaskID)
		case "ws":
			_, ok := response.Payload.(*WSTaskResponse)
			assert.True(t, ok, "response.Payload is not of type WSTaskResponse")
			assert.Equal(t, "another-id", response.TaskID)
		default:
			t.Fail()
		}
	}
}

func TestWatcherExecution_Error(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(echoHandler))
	defer server.Close()

	// Set up URLs
	httpURL, err := url.Parse(server.URL)
	assert.NoError(t, err, "failed to parse HTTP URL")
	header := make(http.Header)

	// Set up watcher
	id := xid.New().String()
	cadence := 1 * time.Second
	var tasks []WatcherTask
	tasks = append(tasks, &MockWatcherTask{
		URL:             httpURL,
		Header:          header,
		ErrTaskResponse: errors.New("mock error"),
		ID:              "an-id",
	})
	watcher, err := NewWatcher(id, cadence, tasks)
	assert.NoError(t, err)

	// Start the watcher and execute the task1
	watcherResponses := make(chan WatcherResponse, 2)
	err = watcher.start(watcherResponses)
	assert.NoError(t, err)
	for _, task := range watcher.Tasks {
		assert.Error(t, task.Task().Execute())
	}

	// Listen for responses on the watcherResponses channel
	response := <-watcherResponses
	assert.NotNil(t, response)
	assert.NotNil(t, response.URL)
	assert.NotNil(t, response.Err)
	assert.Contains(t, response.Err.Error(), "mock error")
	assert.Equal(t, "an-id", response.TaskID)
	assert.Equal(t, id, response.WatcherID)
	assert.Nil(t, response.Payload)
}
