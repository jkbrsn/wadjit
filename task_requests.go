package wadjit

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"sync"

	"github.com/gorilla/websocket"
	"github.com/jakobilobi/go-taskman"
	"github.com/rs/xid"
)

//
// HTTP
//

// HTTPEndpoint represents an HTTP endpoint that can spawn tasks to make requests towards it.
type HTTPEndpoint struct {
	URL    *url.URL
	Header http.Header

	respChan chan<- WatcherResponse
}

// Close closes the HTTP endpoint.
func (e *HTTPEndpoint) Close() error {
	return nil
}

// Initialize sets up the HTTP endpoint to be able to send on its responses.
func (e *HTTPEndpoint) Initialize(responseChannel chan<- WatcherResponse) error {
	e.respChan = responseChannel
	return nil
}

// Task returns a taskman.Task that sends an HTTP request to the endpoint.
func (e *HTTPEndpoint) Task(payload []byte) taskman.Task {
	return &httpRequest{
		Header:   e.Header,
		Method:   http.MethodGet,
		URL:      e.URL,
		Data:     payload,
		respChan: e.respChan,
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
	Header http.Header
	Method string
	URL    *url.URL
	Data   []byte

	respChan chan<- WatcherResponse
}

// Execute sends an HTTP request to the endpoint.
func (r httpRequest) Execute() error {
	request, err := http.NewRequest(r.Method, r.URL.String(), bytes.NewReader(r.Data))
	if err != nil {
		r.respChan <- errorResponse(err, r.URL)
		return err
	}

	for key, values := range r.Header {
		for _, value := range values {
			request.Header.Add(key, value)
		}
	}

	response, err := http.DefaultClient.Do(request)
	if err != nil {
		r.respChan <- errorResponse(err, r.URL)
		return err
	}

	taskResp := NewHTTPTaskResponse(response)

	// Send the response without reading it, leaving that to the Watcher's owner
	r.respChan <- WatcherResponse{
		WatcherID: xid.NilID(),
		URL:       r.URL,
		Err:       nil,
		Payload:   taskResp,
	}

	return nil
}

//
// WebSocket
//

// WSConnection represents a WebSocket connection to a target URL, and can spawn tasks to send
// messages to that endpoint.
// TODO: use read and write deadlines?
// TODO: implement reconnect mechanism
type WSConnection struct {
	mu sync.Mutex

	URL    *url.URL
	Header http.Header

	conn      *websocket.Conn
	ctx       context.Context
	cancel    context.CancelFunc
	writeChan chan []byte
	respChan  chan<- WatcherResponse
}

// Close closes the WebSocket connection, and cancels its context.
func (c *WSConnection) Close() error {
	c.cancel()
	return c.conn.Close()
}

// Initialize sets up the WebSocket connection.
func (c *WSConnection) Initialize(responseChannel chan<- WatcherResponse) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	newConn, _, err := websocket.DefaultDialer.Dial(c.URL.String(), c.Header)
	if err != nil {
		return err
	}
	c.conn = newConn
	c.writeChan = make(chan []byte)
	c.respChan = responseChannel
	c.ctx, c.cancel = context.WithCancel(context.Background())

	go c.read()

	return nil
}

// Task returns a taskman.Task that sends a message to the WebSocket endpoint.
func (c *WSConnection) Task(payload []byte) taskman.Task {
	return &wsSend{
		conn: c,
		msg:  payload,
	}
}

// Validate checks that the WSConnection is ready to be initialized.
func (c *WSConnection) Validate() error {
	if c.URL == nil {
		return errors.New("URL is nil")
	}
	if c.Header == nil {
		// Set empty header if nil
		c.Header = make(http.Header)
	}
	return nil
}

// lock and unlock provide exclusive access to the connection's mutex.
func (c *WSConnection) lock() {
	c.mu.Lock()
}

func (c *WSConnection) unlock() {
	c.mu.Unlock()
}

// read reads messages from the WebSocket connection.
// Note: the read pump has exclusive permission to read from the connection.
func (c *WSConnection) read() {
	defer c.cancel()

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
				}

				// If there was an error, close the connection
				return
			}

			wsResp := NewWSTaskResponse(p)

			// Send the message to the read channel
			response := WatcherResponse{
				WatcherID: xid.NilID(),
				URL:       c.URL,
				Err:       nil,
				Payload:   wsResp,
			}
			c.respChan <- response
		}
	}
}

// wsSend is an implementation to taskman.Task that sends a message to a WebSocket endpoint.
type wsSend struct {
	conn *WSConnection
	msg  []byte
}

// Execute sends a message to the WebSocket endpoint.
// Note: for concurrency safety, the connection's WriteMessage method is used exclusively here.
func (ws *wsSend) Execute() error {
	ws.conn.lock()
	defer ws.conn.unlock()

	select {
	case <-ws.conn.ctx.Done():
		// The connection has been closed
		return nil
	default:
		// Write message to connection
		if err := ws.conn.conn.WriteMessage(websocket.TextMessage, ws.msg); err != nil {
			if websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway) {
				// This is an expected situation, handle gracefully
			} else if strings.Contains(err.Error(), "websocket: close sent") {
				// This is an expected situation, handle gracefully
			} else {
				// This is unexpected
			}

			// TODO: if there was an error, try to reconnect

			ws.conn.respChan <- errorResponse(err, ws.conn.URL)

			return err
		}
	}

	return nil
}
