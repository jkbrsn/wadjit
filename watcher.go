package wadjit

import (
	"bytes"
	"fmt"
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

	// SetUp initializes the watcher and prepares it for use.
	// TODO: rename init?
	// TODO: add response channel to argument list?
	SetUp() error
}

// HTTPWatcher is a watcher that sends HTTP requests to endpoints.
type HTTPWatcher struct {
	id      xid.ID
	cadence time.Duration

	endpoints []Endpoint
	header    http.Header

	payload []byte
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

// SetUp does nothing, but is needed to implement the Watcher interface.
func (w *HTTPWatcher) SetUp() error {
	return nil
}

// WSWatcher is a watcher that sends messages to WebSocket endpoints, and reads the responses.
// TODO: implement a read pump
// TODO: implement a write pump
// TODO: implement a channel to send back responses
// TODO: consider if it's feasible to implement subscriptions, or if a WSSubscriptionWatcher should be created
type WSWatcher struct {
	id      xid.ID
	cadence time.Duration

	connections map[Endpoint]*websocket.Conn // TODO: make sync.Map?
	header      http.Header

	msg []byte
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

// SetUp establishes WebSocket connections to the endpoints of the WSWatcher.
func (w *WSWatcher) SetUp() error {
	var result *multierror.Error
	for endpoint := range w.connections {
		newConn, _, err := websocket.DefaultDialer.Dial(endpoint.URL.Host, w.header)
		if err != nil {
			result = multierror.Append(result, err)
		}
		w.connections[endpoint] = newConn
	}
	return result.ErrorOrNil()
}

// HTTPRequest is an implementation of taskman.Task that sends an HTTP request to an endpoint.
type HTTPRequest struct {
	Header http.Header
	Method string
	URL    *url.URL
	Data   []byte
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
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected status code: %d", response.StatusCode)
	}

	body, err := io.ReadAll(response.Body)
	if err != nil {
		return err
	}
	defer response.Body.Close()

	// TODO: exchange this with a channel send
	fmt.Printf("response: %s\n", body)

	return nil
}

// WSSend is an implementation to taskman.Task that sends a message to a WebSocket endpoint.
type WSSend struct {
	Connection *websocket.Conn
	Message    []byte
	URL        *url.URL

	// TODO: flesh out this channel, e.q. create a write pump, to work around any concurrency issues
	writeChan chan []byte
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
