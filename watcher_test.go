package wadjit

import (
	"io"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/rs/xid"
	"github.com/stretchr/testify/assert"
)

func TestWatcherInitialization(t *testing.T) {
	id := xid.New()
	cadence := 1 * time.Second
	payload := []byte("test payload")
	httpTasks := []HTTPEndpoint{{URL: &url.URL{Scheme: "http", Host: "localhost:8080"}}}
	var tasks []WatcherTask
	for _, task := range httpTasks {
		tasks = append(tasks, &task)
	}
	responses := make(chan WatcherResponse)
	watcher, err := NewWatcher(id, cadence, payload, tasks, responses)
	assert.NoError(t, err)

	assert.Equal(t, id, watcher.ID())
	assert.Equal(t, cadence, watcher.cadence)
	assert.Equal(t, payload, watcher.payload)
	assert.NotNil(t, watcher.doneChan)
	assert.NotNil(t, watcher.taskResponses)
	assert.NotNil(t, watcher.watcherTasks)
}

func TestWatcherStart(t *testing.T) {
	id := xid.New()
	cadence := 1 * time.Second
	payload := []byte("test payload")
	responseChan := make(chan WatcherResponse)

	watcher := &Watcher{
		id:      id,
		cadence: cadence,
		payload: payload,
		watcherTasks: []WatcherTask{
			&HTTPEndpoint{
				URL:    &url.URL{Scheme: "http", Host: "localhost:8080"},
				Header: make(http.Header),
			},
		},
	}

	err := watcher.Start(responseChan)
	assert.NoError(t, err)
	assert.NotNil(t, watcher.watcherTasks)

	// These are not nil, even though uninitialized, since Start initializes them if found nil
	assert.NotNil(t, watcher.doneChan)
	assert.NotNil(t, watcher.taskResponses)
}

func TestWatcherExecution(t *testing.T) {
	server := echoServer()
	defer server.Close()

	// Set up URLs
	httpURL, err := url.Parse(server.URL)
	assert.NoError(t, err, "failed to parse HTTP URL")
	wsURL, err := url.Parse("ws" + server.URL[4:] + "/ws")
	assert.NoError(t, err, "failed to parse WS URL")
	header := make(http.Header)

	// Set up watcher
	id := xid.New()
	cadence := 1 * time.Second
	payload := []byte("test payload")
	var tasks []WatcherTask
	tasks = append(tasks, &HTTPEndpoint{URL: httpURL, Header: header})
	tasks = append(tasks, &WSConnection{URL: wsURL, Header: header})
	taskResponses := make(chan WatcherResponse)
	watcher, err := NewWatcher(id, cadence, payload, tasks, taskResponses)
	assert.NoError(t, err)

	// Start the watcher and execute the tasks
	watcherResponses := make(chan WatcherResponse)
	err = watcher.Start(watcherResponses)
	assert.NoError(t, err)
	for _, task := range watcher.watcherTasks {
		task.Task(payload).Execute()
	}

	// Listen for responses on the watcherResponses channel
	for i := 0; i < len(watcher.watcherTasks); i++ {
		response := <-watcherResponses
		assert.NotNil(t, response)
		assert.NotNil(t, response.URL)
		assert.Nil(t, response.Err)
		assert.Equal(t, id, response.WatcherID)
		if response.URL.Scheme == "http" {
			assert.NotNil(t, response.HTTPResponse)
			responsePayload, err := io.ReadAll(response.HTTPResponse.Body)
			assert.NoError(t, err)
			assert.Equal(t, payload, responsePayload)
		} else if response.URL.Scheme == "ws" {
			assert.Equal(t, payload, response.WSData)
		} else {
			t.Fail()
		}
	}
}

func TestHTTPEndpointInitialize(t *testing.T) {
	url, _ := url.Parse("http://example.com")
	header := make(http.Header)
	responseChan := make(chan WatcherResponse)

	endpoint := &HTTPEndpoint{
		URL:    url,
		Header: header,
	}

	assert.Equal(t, url, endpoint.URL)
	assert.Equal(t, header, endpoint.Header)
	assert.Nil(t, endpoint.respChan)

	err := endpoint.Initialize(responseChan)
	assert.NoError(t, err)
	assert.NotNil(t, endpoint.respChan)
}

func TestWSConnectionInitialize(t *testing.T) {
	server := echoServer()
	defer server.Close()

	wsURL := "ws" + server.URL[4:] + "/ws"
	url, err := url.Parse(wsURL)
	assert.NoError(t, err, "failed to parse URL")
	header := make(http.Header)
	responseChan := make(chan WatcherResponse)

	conn := &WSConnection{
		URL:    url,
		Header: header,
	}

	assert.Equal(t, url, conn.URL)
	assert.Equal(t, header, conn.Header)
	assert.Nil(t, conn.respChan)

	err = conn.Initialize(responseChan)
	assert.NoError(t, err)
	assert.NotNil(t, conn.respChan)
	assert.NotNil(t, conn.conn)
	assert.NotNil(t, conn.writeChan)
	assert.NotNil(t, conn.ctx)
	assert.NotNil(t, conn.cancel)
}
