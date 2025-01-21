package wadjit

import (
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

// TODO: uncomment when a WS test server is available
/* func TestWSConnectionInitialize(t *testing.T) {
	url, _ := url.Parse("ws://example.com/socket")
	header := make(http.Header)
	responseChan := make(chan WatcherResponse)

	conn := &WSConnection{
		URL:    url,
		Header: header,
	}

	assert.Equal(t, url, conn.URL)
	assert.Equal(t, header, conn.Header)
	assert.Nil(t, conn.respChan)

	err := conn.Initialize(responseChan)
	assert.NoError(t, err)
	assert.NotNil(t, conn.respChan)
	assert.NotNil(t, conn.conn)
	assert.NotNil(t, conn.writeChan)
	assert.NotNil(t, conn.ctx)
	assert.NotNil(t, conn.cancel)
} */
