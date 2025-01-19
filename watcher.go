package wadjit

import (
	"bytes"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/gorilla/websocket"
	"github.com/hashicorp/go-multierror"
	"github.com/jakobilobi/go-taskman"
	"github.com/rs/xid"
)

// Watcher is an interface that represents a watcher.
type Watcher interface {
	// Close closes the watcher and releases any resources associated with it.
	Close() error

	// ID returns the unique identifier of the watcher.
	ID() xid.ID

	// Job returns the job which defines the tasks that the watcher will use.
	Job() taskman.Job

	// Initialize sets up the watcher to listen for responses, which are sent to responseChan.
	Initialize(responseChan chan WatcherResponse) error
}

// WatcherResponse represents a response from a watcher.
// TODO: evaluate if this is the cleanest way of merging HTTP and WS responses under the same struct
type WatcherResponse struct {
	WatcherID xid.ID
	Endpoint  *url.URL
	Err       error
	Data      []byte        // For WS or if HTTP is pre-read
	Body      io.ReadCloser // For streaming HTTP, may be nil for WS

	// TOOD: possibly add a "Protocol" or "Type" field to distinguish between HTTP and WS
}

// HTTPWatcher is a watcher that sends HTTP requests to endpoints.
type HTTPWatcher struct {
	id        xid.ID
	endpoints []Endpoint

	cadence time.Duration
	header  http.Header
	payload []byte

	respChan chan httpResponse
	doneChan chan struct{}
}

// Close does nothing, but is needed to implement the Watcher interface.
func (w *HTTPWatcher) Close() error {
	return nil
}

// ID returns the ID of the HTTPWatcher.
func (w *HTTPWatcher) ID() xid.ID {
	return w.id
}

// Job returns a taskman.Job that sends HTTP requests to the endpoints of the HTTPWatcher.
func (w *HTTPWatcher) Job() taskman.Job {
	tasks := make([]taskman.Task, 0, len(w.endpoints))
	for _, endpoint := range w.endpoints {
		tasks = append(tasks, HTTPRequest{
			Method: http.MethodGet,
			URL:    endpoint.URL,
			Header: w.header,
			Data:   w.payload,
		})
	}
	job := taskman.Job{
		ID:       w.id.String(),
		Cadence:  w.cadence,
		NextExec: time.Now().Add(w.cadence),
		Tasks:    tasks,
	}
	return job
}

// Initialize sets up a forwarder for responses from the watcher's HTTP requests.
func (w *HTTPWatcher) Initialize(responseChan chan WatcherResponse) error {
	go w.forwardResponses(responseChan)
	return nil
}

// forwardResponses listens for responses from the HTTPWatcher's requests, and forwards them to
// the responseChan.
func (w *HTTPWatcher) forwardResponses(responseChan chan WatcherResponse) {
	for {
		select {
		case resp := <-w.respChan:
			response := WatcherResponse{
				WatcherID: w.id,
				Endpoint:  resp.url,
				Err:       nil,
				Data:      nil,
				Body:      resp.resp.Body,
			}
			responseChan <- response
		case <-w.doneChan:
			return
		}
	}
}

// WSWatcher is a watcher that sends messages to WebSocket endpoints, and reads the responses.
// TODO: implement a read pump
// TODO: implement a write pump
// TODO: implement a channel to send back responses
// TODO: consider if it's feasible to implement subscriptions, or if a WSSubscriptionWatcher should be created
type WSWatcher struct {
	id          xid.ID
	connections map[Endpoint]*websocket.Conn // TODO: make sync.Map?

	cadence time.Duration
	header  http.Header
	msg     []byte

	respChan chan wsRead
	doneChan chan struct{}
}

// Close closes all WebSocket connections.
func (w *WSWatcher) Close() error {
	var result *multierror.Error
	for _, conn := range w.connections {
		err := conn.Close()
		if err != nil {
			result = multierror.Append(result, err)
		}
	}
	return result.ErrorOrNil()
}

// ID returns the ID of the watcher.
func (w *WSWatcher) ID() xid.ID {
	return w.id
}

// Job returns a taskman.Job that sends messages to WebSocket endpoints.
func (w *WSWatcher) Job() taskman.Job {
	tasks := make([]taskman.Task, 0, len(w.connections))
	for endpoint, conn := range w.connections {
		tasks = append(tasks, WSSend{
			URL:        endpoint.URL,
			Message:    w.msg,
			Connection: conn,
		})
	}
	job := taskman.Job{
		ID:       w.id.String(),
		Cadence:  w.cadence,
		NextExec: time.Now().Add(w.cadence),
		Tasks:    tasks,
	}
	return job
}

// Initialize establishes WebSocket connections to the endpoints of the WSWatcher, starts the
// goroutines that read and write to the connections, and sets up a forwarder for responses.
func (w *WSWatcher) Initialize(responseChan chan WatcherResponse) error {
	var result *multierror.Error

	// Establish connections to the endpoints
	for endpoint := range w.connections {
		newConn, _, err := websocket.DefaultDialer.Dial(endpoint.URL.Host, w.header)
		if err != nil {
			result = multierror.Append(result, err)
		}
		w.connections[endpoint] = newConn
	}

	// TODO: start write pump
	// TODO: start read pump

	go w.forwardReads(responseChan)

	return result.ErrorOrNil()
}

// forwardReads listens for responses from the HTTPWatcher's HTTP requests.
func (w *WSWatcher) forwardReads(responseChan chan WatcherResponse) {
	for {
		select {
		case resp := <-w.respChan:
			response := WatcherResponse{
				WatcherID: w.id,
				Endpoint:  resp.url,
				Err:       nil,
				Data:      resp.data,
				Body:      nil,
			}
			responseChan <- response
		case <-w.doneChan:
			return
		}
	}
}

// HTTP REQUESTS
// TODO: move to a separate file

// HTTPRequest is an implementation of taskman.Task that sends an HTTP request to an endpoint.
type HTTPRequest struct {
	Header http.Header
	Method string
	URL    *url.URL
	Data   []byte

	respChan chan httpResponse
}

type httpResponse struct {
	resp *http.Response
	url  *url.URL
}

// Execute sends an HTTP request to the endpoint.
func (r HTTPRequest) Execute() error {
	request, err := http.NewRequest(r.Method, r.URL.String(), bytes.NewReader(r.Data))
	if err != nil {
		return err
	}

	for key, values := range r.Header {
		for _, value := range values {
			request.Header.Add(key, value)
		}
	}

	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return err
	}

	// Send the response without reading it, leaving that to the Watcher's owner
	r.respChan <- httpResponse{
		resp: response,
		url:  r.URL,
	}

	return nil
}

// WEBSOCKETS
// TODO: move to a separate file

// WSSend is an implementation to taskman.Task that sends a message to a WebSocket endpoint.
type WSSend struct {
	Connection *websocket.Conn
	Message    []byte
	URL        *url.URL

	// TODO: flesh out this channel, e.q. create a write pump, to work around any concurrency issues
	writeChan chan []byte
}

type wsRead struct {
	data []byte
	url  *url.URL
}

// Execute sends a message to the WebSocket endpoint.
func (w WSSend) Execute() error {
	err := w.Connection.WriteMessage(websocket.TextMessage, w.Message)
	if err != nil {
		return err
	}

	msg := make([]byte, 512)
	w.writeChan <- msg

	return nil
}
