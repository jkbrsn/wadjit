package wadjit

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/jakobilobi/go-taskman"
	"github.com/rs/xid"
)

// WatcherTask is a task that the Watcher can execute to interact with a target endpoint.
type WatcherTask interface {
	// Close closes the WatcherTask, cleaning up and releasing resources.
	// Note: will block until the task is closed. (?)
	Close() error

	// Initialize sets up the WatcherTask to be ready to watch an endpoint.
	Initialize(id xid.ID, respChan chan<- WatcherResponse) error

	// Task returns a taskman.Task that sends requests and messages to the endpoint.
	Task() taskman.Task

	// Validate checks that the WatcherTask is ready for initialization.
	Validate() error
}

//
// HTTP
//

// HTTPEndpoint spawns tasks to make HTTP requests towards the defined endpoint.
type HTTPEndpoint struct {
	URL     *url.URL
	Header  http.Header
	Payload []byte

	id       xid.ID
	respChan chan<- WatcherResponse
}

// Close closes the HTTP endpoint.
func (e *HTTPEndpoint) Close() error {
	return nil
}

// Initialize sets up the HTTP endpoint to be able to send on its responses.
func (e *HTTPEndpoint) Initialize(id xid.ID, responseChannel chan<- WatcherResponse) error {
	e.id = id
	e.respChan = responseChannel
	// TODO: set mode based on payload, e.g. JSON RPC, text ete.
	return nil
}

// Task returns a taskman.Task that sends an HTTP request to the endpoint.
func (e *HTTPEndpoint) Task() taskman.Task {
	return &httpRequest{
		endpoint: e,
		respChan: e.respChan,
		data:     e.Payload,
		method:   http.MethodGet,
	}
}

// Validate checks that the HTTPEndpoint is ready to be initialized.
func (e *HTTPEndpoint) Validate() error {
	if e.URL == nil {
		return errors.New("URL is nil")
	}
	if e.Header == nil {
		// Set empty header if nil
		e.Header = make(http.Header)
	}
	return nil
}

// httpRequest is an implementation of taskman.Task that sends an HTTP request to an endpoint.
type httpRequest struct {
	endpoint *HTTPEndpoint
	respChan chan<- WatcherResponse

	data   []byte
	method string
}

// Execute sends an HTTP request to the endpoint.
func (r httpRequest) Execute() error {
	request, err := http.NewRequest(r.method, r.endpoint.URL.String(), bytes.NewReader(r.data))
	if err != nil {
		r.respChan <- errorResponse(err, r.endpoint.id, r.endpoint.URL)
		return err
	}

	for key, values := range r.endpoint.Header {
		for _, value := range values {
			request.Header.Add(key, value)
		}
	}

	// TODO: measure time taken to send request, perhaps all stages of the request

	response, err := http.DefaultClient.Do(request)
	if err != nil {
		r.respChan <- errorResponse(err, r.endpoint.id, r.endpoint.URL)
		return err
	}

	taskResp := NewHTTPTaskResponse(response)

	// Send the response without reading it, leaving that to the Watcher's owner
	r.respChan <- WatcherResponse{
		WatcherID: r.endpoint.id,
		URL:       r.endpoint.URL,
		Err:       nil,
		Payload:   taskResp,
	}

	return nil
}

//
// WebSocket
//

// WSEndpoint connects to the target endpoint, and spawns tasks to send messages
// to that endpoint.
type WSEndpoint struct {
	mu sync.Mutex

	URL     *url.URL
	Header  http.Header
	Payload []byte

	conn      *websocket.Conn
	ctx       context.Context
	cancel    context.CancelFunc
	wg        sync.WaitGroup
	writeChan chan []byte

	id       xid.ID
	respChan chan<- WatcherResponse
}

// Close closes the WebSocket connection, and cancels its context.
func (e *WSEndpoint) Close() error {
	e.lock()
	defer e.unlock()

	// If the connection is already closed, do nothing
	if e.conn == nil {
		return nil
	}

	// Close the connection
	formattedCloseMessage := websocket.FormatCloseMessage(websocket.CloseNormalClosure, "")
	deadline := time.Now().Add(3 * time.Second)
	err := e.conn.WriteControl(websocket.CloseMessage, formattedCloseMessage, deadline)
	if err != nil {
		return err
	}
	err = e.conn.Close()
	if err != nil {
		return err
	}
	e.conn = nil

	// Cancel the context
	e.cancel()

	return nil
}

// Initialize sets up the WebSocket connection.
func (e *WSEndpoint) Initialize(id xid.ID, responseChannel chan<- WatcherResponse) error {
	e.mu.Lock()
	e.id = id
	e.writeChan = make(chan []byte)
	e.respChan = responseChannel
	// TODO: set mode based on payload, e.g. JSON RPC, text ete.
	e.mu.Unlock()

	err := e.connect()
	if err != nil {
		return fmt.Errorf("failed to connect when initializing: %w", err)
	}

	return nil
}

// Task returns a taskman.Task that sends a message to the WebSocket endpoint.
func (e *WSEndpoint) Task() taskman.Task {
	return &wsLongConn{
		wsEndpoint: e,
		msg:        e.Payload,
	}
}

// Validate checks that the WSEndpoint is ready to be initialized.
func (e *WSEndpoint) Validate() error {
	if e.URL == nil {
		return errors.New("URL is nil")
	}
	if e.Header == nil {
		// Set empty header if nil
		e.Header = make(http.Header)
	}
	return nil
}

// connect establishes a connection to the WebSocket endpoint. If already connected,
// this function does nothing.
func (e *WSEndpoint) connect() error {
	e.mu.Lock()
	defer e.mu.Unlock()

	// Only connect if the connection is not already established
	if e.conn != nil || e.ctx != nil {
		return fmt.Errorf("connection already established")
	}

	// Establish the connection
	conn, _, err := websocket.DefaultDialer.Dial(e.URL.String(), e.Header)
	if err != nil {
		return err
	}
	e.conn = conn

	// Set up the context and read pump
	e.ctx, e.cancel = context.WithCancel(context.Background())

	// Start the read pump for incoming messages
	e.wg.Add(1)
	go e.readPump(&e.wg)

	return nil
}

// reconnect closes the current connection and establishes a new one.
func (e *WSEndpoint) reconnect() error {
	e.mu.Lock()
	defer e.mu.Unlock()

	// Cancel the current context
	if e.cancel != nil {
		e.cancel()
	}

	// Close the current connection, if it exists
	if e.conn != nil {
		if err := e.conn.Close(); err != nil {
			return fmt.Errorf("failed to close connection: %w", err)
		}
	}

	// Wait for the read pump to finish
	e.wg.Wait()

	// Establish a new connection
	conn, _, err := websocket.DefaultDialer.Dial(e.URL.String(), e.Header)
	if err != nil {
		return fmt.Errorf("failed to dial when reconnecting: %w", err)
	}
	e.conn = conn

	// Set up a new context and read pump
	e.ctx, e.cancel = context.WithCancel(context.Background())

	// Restart the read pump for incoming messages
	e.wg.Add(1)
	go e.readPump(&e.wg)

	return nil
}

// lock and unlock provide exclusive access to the connection's mutex.
func (e *WSEndpoint) lock() {
	e.mu.Lock()
}

func (e *WSEndpoint) unlock() {
	e.mu.Unlock()
}

// read reads messages from the WebSocket connection.
// Note: the read pump has exclusive permission to read from the connection.
func (e *WSEndpoint) readPump(wg *sync.WaitGroup) {
	defer func() {
		wg.Done()
	}()

	for {
		select {
		case <-e.ctx.Done():
			return
		default:
			// Read message from connection
			_, p, err := e.conn.ReadMessage()
			if err != nil {
				if websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway, websocket.CloseServiceRestart) {
					// This is an expected situation, handle gracefully
				} else if strings.Contains(err.Error(), "connection closed") {
					// This is not an unknown situation, handle gracefully
				} else {
					// This is unexpected
					// TODO: add custom handling here?
				}

				// If there was an error, close the connection
				return
			}

			// TODO: if WSEndpoint is set to "JSON RPC mode":
			// 1. unmarshal p into a JSON-RPC interface
			// 2. check the id against the "inflight map" in WSEndpoint
			// 3. if the id is found, get the inflight map metadata and delete the id in the map
			// 4. restore original id and marshal the JSON-RPC interface back into text message
			// 5. set metadata to the taskresponse: original id, duration between time sent and time received

			wsResp := NewWSTaskResponse(p)

			// Send the message to the read channel
			response := WatcherResponse{
				WatcherID: e.id,
				URL:       e.URL,
				Err:       nil,
				Payload:   wsResp,
			}
			e.respChan <- response
		}
	}
}

// wsLongConn is an implementation of taskman.Task that sends a message on a persistent
// WebSocket connection.
type wsLongConn struct {
	wsEndpoint *WSEndpoint

	msg []byte
}

// Execute sends a message to the WebSocket endpoint.
// Note: for concurrency safety, the connection's WriteMessage method is used exclusively here.
func (wlc *wsLongConn) Execute() error {
	// If the connection is closed, try to reconnect
	if wlc.wsEndpoint.conn == nil {
		if err := wlc.wsEndpoint.reconnect(); err != nil {
			return err
		}
	}

	wlc.wsEndpoint.lock()
	defer wlc.wsEndpoint.unlock()

	select {
	case <-wlc.wsEndpoint.ctx.Done():
		// The connection has been closed
		return nil
	default:

		// TODO: if WSEndpoint is set to "JSON RPC mode":
		// 1. unmarsal ws.msg into a JSON-RPC interface
		// 2. set the id to a something randomly generated
		// 3. store the id in a "inflight map" in WSEndpoint, with metadata: original id, time sent
		// 4. marshal the JSON-RPC interface back into text message

		// Write message to connection
		if err := wlc.wsEndpoint.conn.WriteMessage(websocket.TextMessage, wlc.msg); err != nil {
			if websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway) {
				// This is an expected situation, handle gracefully
			} else if strings.Contains(err.Error(), "websocket: close sent") {
				// This is an expected situation, handle gracefully
			} else {
				// This is unexpected
				// TODO: add custom handling here?
			}

			// If there was an error, close the connection
			wlc.wsEndpoint.Close()

			// Send an error response
			wlc.wsEndpoint.respChan <- errorResponse(err, wlc.wsEndpoint.id, wlc.wsEndpoint.URL)
			return err
		}
	}

	return nil
}

// TODO: create second task implementation for "instant mode", that both sends and reads messages in the same task

// errorResponse is a helper to create a WatcherResponse with an error.
func errorResponse(err error, id xid.ID, url *url.URL) WatcherResponse {
	return WatcherResponse{
		WatcherID: id,
		URL:       url,
		Err:       err,
		Payload:   nil,
	}
}
