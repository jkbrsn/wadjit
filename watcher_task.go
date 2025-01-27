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

// HTTPEndpoint represents an HTTP endpoint that can spawn tasks to make requests towards it.
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
	// TODO: set mode based on payload, e.g. JSON RPC, text etc.
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

// wsConn represents a WebSocket connection to a target URL, and can spawn tasks to send
// messages to that endpoint.
type wsConn struct {
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
func (c *wsConn) Close() error {
	c.lock()
	defer c.unlock()

	// If the connection is already closed, do nothing
	if c.conn == nil {
		return nil
	}

	// Close the connection
	formattedCloseMessage := websocket.FormatCloseMessage(websocket.CloseNormalClosure, "")
	deadline := time.Now().Add(3 * time.Second)
	err := c.conn.WriteControl(websocket.CloseMessage, formattedCloseMessage, deadline)
	if err != nil {
		return err
	}
	err = c.conn.Close()
	if err != nil {
		return err
	}
	c.conn = nil

	// Cancel the context
	c.cancel()

	return nil
}

// Initialize sets up the WebSocket connection.
func (c *wsConn) Initialize(id xid.ID, responseChannel chan<- WatcherResponse) error {
	c.mu.Lock()
	c.id = id
	c.writeChan = make(chan []byte)
	c.respChan = responseChannel
	// TODO: set mode based on payload, e.g. JSON RPC, text etc.
	c.mu.Unlock()

	err := c.connect()
	if err != nil {
		return fmt.Errorf("failed to connect when initializing: %w", err)
	}

	return nil
}

// Task returns a taskman.Task that sends a message to the WebSocket endpoint.
func (c *wsConn) Task() taskman.Task {
	return &wsSend{
		conn: c,
		msg:  c.Payload,
	}
}

// Validate checks that the wsConn is ready to be initialized.
func (c *wsConn) Validate() error {
	if c.URL == nil {
		return errors.New("URL is nil")
	}
	if c.Header == nil {
		// Set empty header if nil
		c.Header = make(http.Header)
	}
	return nil
}

// connect establishes a connection to the WebSocket endpoint. If already connected,
// this function does nothing.
func (c *wsConn) connect() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Only connect if the connection is not already established
	if c.conn != nil || c.ctx != nil {
		return fmt.Errorf("connection already established")
	}

	// Establish the connection
	conn, _, err := websocket.DefaultDialer.Dial(c.URL.String(), c.Header)
	if err != nil {
		return err
	}
	c.conn = conn

	// Set up the context and read pump
	c.ctx, c.cancel = context.WithCancel(context.Background())

	// Start the read pump for incoming messages
	c.wg.Add(1)
	go c.readPump(&c.wg)

	return nil
}

// reconnect closes the current connection and establishes a new one.
func (c *wsConn) reconnect() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Cancel the current context
	if c.cancel != nil {
		c.cancel()
	}

	// Close the current connection, if it exists
	if c.conn != nil {
		if err := c.conn.Close(); err != nil {
			return fmt.Errorf("failed to close connection: %w", err)
		}
	}

	// Wait for the read pump to finish
	c.wg.Wait()

	// Establish a new connection
	conn, _, err := websocket.DefaultDialer.Dial(c.URL.String(), c.Header)
	if err != nil {
		return fmt.Errorf("failed to dial when reconnecting: %w", err)
	}
	c.conn = conn

	// Set up a new context and read pump
	c.ctx, c.cancel = context.WithCancel(context.Background())

	// Restart the read pump for incoming messages
	c.wg.Add(1)
	go c.readPump(&c.wg)

	return nil
}

// lock and unlock provide exclusive access to the connection's mutex.
func (c *wsConn) lock() {
	c.mu.Lock()
}

func (c *wsConn) unlock() {
	c.mu.Unlock()
}

// read reads messages from the WebSocket connection.
// Note: the read pump has exclusive permission to read from the connection.
func (c *wsConn) readPump(wg *sync.WaitGroup) {
	defer func() {
		wg.Done()
	}()

	for {
		select {
		case <-c.ctx.Done():
			return
		default:
			// Read message from connection
			_, p, err := c.conn.ReadMessage()
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

			// TODO: if wsConn is set to "JSON RPC mode":
			// 1. unmarshal p into a JSON-RPC interface
			// 2. check the id against the "inflight map" in wsConn
			// 3. if the id is found, get the inflight map metadata and delete the id in the map
			// 4. restore original id and marshal the JSON-RPC interface back into text message
			// 5. set metadata to the taskresponse: original id, duration between time sent and time received

			wsResp := NewWSTaskResponse(p)

			// Send the message to the read channel
			response := WatcherResponse{
				WatcherID: c.id,
				URL:       c.URL,
				Err:       nil,
				Payload:   wsResp,
			}
			c.respChan <- response
		}
	}
}

// TODO: create second task implementation for "instant mode", that both sends and reads messages in the same task

// wsSend is an implementation to taskman.Task that sends a message to a WebSocket endpoint.
type wsSend struct {
	conn *wsConn

	msg []byte
}

// Execute sends a message to the WebSocket endpoint.
// Note: for concurrency safety, the connection's WriteMessage method is used exclusively here.
func (ws *wsSend) Execute() error {
	// If the connection is closed, try to reconnect
	if ws.conn.conn == nil {
		if err := ws.conn.reconnect(); err != nil {
			return err
		}
	}

	ws.conn.lock()
	defer ws.conn.unlock()

	select {
	case <-ws.conn.ctx.Done():
		// The connection has been closed
		return nil
	default:

		// TODO: if wsConn is set to "JSON RPC mode":
		// 1. unmarsal ws.msg into a JSON-RPC interface
		// 2. set the id to a something randomly generated
		// 3. store the id in a "inflight map" in wsConn, with metadata: original id, time sent
		// 4. marshal the JSON-RPC interface back into text message

		// Write message to connection
		if err := ws.conn.conn.WriteMessage(websocket.TextMessage, ws.msg); err != nil {
			if websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway) {
				// This is an expected situation, handle gracefully
			} else if strings.Contains(err.Error(), "websocket: close sent") {
				// This is an expected situation, handle gracefully
			} else {
				// This is unexpected
				// TODO: add custom handling here?
			}

			// If there was an error, close the connection
			ws.conn.Close()

			// Send an error response
			ws.conn.respChan <- errorResponse(err, ws.conn.id, ws.conn.URL)
			return err
		}
	}

	return nil
}

// errorResponse is a helper to create a WatcherResponse with an error.
func errorResponse(err error, id xid.ID, url *url.URL) WatcherResponse {
	return WatcherResponse{
		WatcherID: id,
		URL:       url,
		Err:       err,
		Payload:   nil,
	}
}
