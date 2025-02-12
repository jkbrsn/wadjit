package wadjit

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/bytedance/sonic"
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

	// Add tracing to the request
	timings := &RequestTimes{}
	trace := traceRequest(timings)
	ctx := httptrace.WithClientTrace(request.Context(), trace)
	request = request.WithContext(ctx)

	// Add headers to the request
	for key, values := range r.endpoint.Header {
		for _, value := range values {
			request.Header.Add(key, value)
		}
	}

	// Send the request
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		r.respChan <- errorResponse(err, r.endpoint.id, r.endpoint.URL)
		return err
	}

	// Create a task response
	taskResponse := NewHTTPTaskResponse(response)
	taskResponse.latency = timings.FirstResponseByte.Sub(timings.Start)
	taskResponse.receivedAt = time.Now()

	// Send the response on the channel
	r.respChan <- WatcherResponse{
		WatcherID: r.endpoint.id,
		URL:       r.endpoint.URL,
		Err:       nil,
		Payload:   taskResponse,
	}

	return nil
}

// RequestTimes stores the timestamps of an HTTP request used to time latency.
type RequestTimes struct {
	Start             time.Time // When the request started
	FirstResponseByte time.Time // When the first byte of the response was received
}

func traceRequest(times *RequestTimes) *httptrace.ClientTrace {
	return &httptrace.ClientTrace{
		// The earliest guaranteed callback is usually ConnectStart, so we set the start time there
		ConnectStart: func(_, _ string) {
			times.Start = time.Now()
		},
		GotFirstResponseByte: func() {
			times.FirstResponseByte = time.Now()
		},
	}
}

//
// WebSocket
//

// WSEndpoint connects to the target endpoint, and spawns tasks to send messages
// to that endpoint.
type WSEndpoint struct {
	mu   sync.Mutex
	mode WSEndpointMode

	URL     *url.URL
	Header  http.Header
	Payload []byte

	conn         *websocket.Conn
	ctx          context.Context
	cancel       context.CancelFunc
	inflightMsgs sync.Map // Key string to value WSInflightMessage
	wg           sync.WaitGroup

	id       xid.ID
	respChan chan<- WatcherResponse
}

// WSEndpointMode is an enum for the mode of the WebSocket endpoint.
type WSEndpointMode int

const (
	ModeUnknown      WSEndpointMode = iota // Defaults to ModeText
	OneHitText                             // One hit text mode is the default mode
	LongLivedJSONRPC                       // Long lived JSON RPC mode
)

// WSInflightMessage stores metadata about a message that is currently in-flight.
type WSInflightMessage struct {
	inflightID string
	originalID interface{}
	timeSent   time.Time
}

// Close closes the WebSocket connection, and cancels its context.
func (e *WSEndpoint) Close() error {
	e.lock()
	defer e.unlock()

	// If the connection is already closed, do nothing
	if e.conn == nil {
		return nil
	} else {
		// Close the connection
		formattedCloseMessage := websocket.FormatCloseMessage(websocket.CloseNormalClosure, "")
		deadline := time.Now().Add(3 * time.Second)
		err := e.conn.WriteControl(websocket.CloseMessage, formattedCloseMessage, deadline)
		if err != nil {
			return err
		}
		// TODO: formally wait for response?
		err = e.conn.Close()
		if err != nil {
			return err
		}
		e.conn = nil
	}

	// Cancel the context
	e.cancel()

	return nil
}

// Initialize prepares the WSEndpoint to be able to send messages to the target endpoint.
// If configured as one of the persistent connection modes, e.g. JSON RPC, this function will
// establish a long-lived connection to the endpoint.
func (e *WSEndpoint) Initialize(id xid.ID, responseChannel chan<- WatcherResponse) error {
	e.mu.Lock()
	e.id = id
	e.respChan = responseChannel
	e.ctx, e.cancel = context.WithCancel(context.Background())
	e.mu.Unlock()

	switch e.mode {
	case LongLivedJSONRPC:
		err := e.connect()
		if err != nil {
			return fmt.Errorf("failed to connect when initializing: %w", err)
		}
	case OneHitText:
		// One hit modes do not require a connection to be established, so do nothing
	default:
		// Default to one hit text mode, since its a mode not requiring anything logic outside of its own scope
		e.mu.Lock()
		e.mode = OneHitText
		e.mu.Unlock()
	}

	return nil
}

// Task returns a taskman.Task that sends a message to the WebSocket endpoint.
func (e *WSEndpoint) Task() taskman.Task {
	switch e.mode {
	case OneHitText:
		return &wsShortConn{
			wsEndpoint: e,
		}
	case LongLivedJSONRPC:
		return &wsLongConn{
			wsEndpoint: e,
		}
	default:
		// Default to text mode
		// TODO: this probably works alright, but maybe redesign to return error instead?
		return &wsShortConn{
			wsEndpoint: e,
		}
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
	if e.mode == OneHitText {
		return errors.New("cannot establish long connection in text mode")
	}
	e.mu.Lock()
	defer e.mu.Unlock()

	// Only connect if the connection is not already established
	if e.conn != nil {
		return fmt.Errorf("connection already established")
	}

	// Establish the connection
	conn, _, err := websocket.DefaultDialer.Dial(e.URL.String(), e.Header)
	if err != nil {
		return err
	}
	e.conn = conn

	// Start the read pump for incoming messages
	e.wg.Add(1)
	go e.readPump(&e.wg)

	return nil
}

// reconnect closes the current connection and establishes a new one.
func (e *WSEndpoint) reconnect() error {
	if e.mode == OneHitText {
		return errors.New("cannot re-establish long connection in text mode")
	}
	e.mu.Lock()
	defer e.mu.Unlock()

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
			// Endpoint shutting down
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
			timeRead := time.Now()

			// TODO: limit these steps to JSON RPC mode

			// 1. Unmarshal p into a JSON-RPC response interface
			jsonRPCResp := &JSONRPCResponse{}
			err = jsonRPCResp.ParseFromBytes(p, len(p))
			if err != nil {
				// Send an error response
				e.respChan <- errorResponse(err, e.id, e.URL)
				return
			}

			// 2. Check the ID against the inflight messages map
			if !jsonRPCResp.IsEmpty() {
				responseID := jsonRPCResp.ID()
				if responseID == nil {
					// Send an error response
					e.respChan <- errorResponse(err, e.id, e.URL)
					return
				}
				responseIDStr := fmt.Sprintf("%v", responseID)

				// 3. If the ID is known, get the inflight map metadata and delete the ID in the map
				if inflightMsg, ok := e.inflightMsgs.Load(responseIDStr); ok {
					inflightMsg := inflightMsg.(WSInflightMessage)
					latency := timeRead.Sub(inflightMsg.timeSent)
					e.inflightMsgs.Delete(responseIDStr)

					// 4. Restore original ID and marshal the JSON-RPC interface back into a byte slice
					jsonRPCResp.id = inflightMsg.originalID
					p, err = jsonRPCResp.MarshalJSON()
					if err != nil {
						// Send an error response
						e.respChan <- errorResponse(err, e.id, e.URL)
						return
					}
					// 5. set metadata to the taskresponse: original id, duration between time sent and time received
					taskResponse := NewWSTaskResponse(p)
					taskResponse.latency = latency
					taskResponse.receivedAt = timeRead

					// Send the message to the read channel
					response := WatcherResponse{
						WatcherID: e.id,
						URL:       e.URL,
						Err:       nil,
						Payload:   taskResponse,
					}
					e.respChan <- response
				}
			} else {
				// Send the message to the read channel
				response := WatcherResponse{
					WatcherID: e.id,
					URL:       e.URL,
					Err:       nil,
					Payload:   NewWSTaskResponse(p),
				}
				e.respChan <- response
			}
		}
	}
}

// wsLongConn is an implementation of taskman.Task that sends a message on a persistent
// WebSocket connection.
// TODO: rename/rebrand into a wsJSONRPC struct, or something like that
type wsLongConn struct {
	wsEndpoint *WSEndpoint
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
		// Endpoint shutting down, do nothing
		return nil
	default:
		// Prepare shadowed variables for the message
		var payload []byte
		var err error

		// TODO: limit these steps to JSON RPC mode
		if wlc.wsEndpoint.mode == LongLivedJSONRPC {
			// 1. Unmarshal the msg into a JSON-RPC interface
			jsonRPCReq := &JSONRPCRequest{}
			if len(wlc.wsEndpoint.Payload) > 0 {
				// TODO: optimize this to only get the ID?
				err := jsonRPCReq.UnmarshalJSON(wlc.wsEndpoint.Payload)
				if err != nil {
					wlc.wsEndpoint.respChan <- errorResponse(err, wlc.wsEndpoint.id, wlc.wsEndpoint.URL)
					return fmt.Errorf("failed to unmarshal JSON-RPC message: %w", err)
				}
			}

			// 2. Generate a random ID and extract the original ID from the JSON-RPC interface
			inflightID := xid.New().String()
			var originalID interface{}
			if !jsonRPCReq.IsEmpty() {
				originalID = jsonRPCReq.ID
				jsonRPCReq.ID = inflightID
			}

			// 3. store the id in a "inflight map" in WSEndpoint, with metadata: original id, time sent
			inflightMsg := WSInflightMessage{
				inflightID: inflightID,
				originalID: originalID,
			}

			// 4. Marshal the updated JSON-RPC interface back into text message
			payload, err = sonic.Marshal(jsonRPCReq)
			if err != nil {
				wlc.wsEndpoint.respChan <- errorResponse(err, wlc.wsEndpoint.id, wlc.wsEndpoint.URL)
				return fmt.Errorf("failed to marshal JSON-RPC message: %w", err)
			}
			inflightMsg.timeSent = time.Now()

			// 5. Store the inflight message in the WSEndpoint
			wlc.wsEndpoint.inflightMsgs.Store(inflightID, inflightMsg)
		}

		// If the payload is nil, use the endpoint's payload
		if payload == nil {
			payload = wlc.wsEndpoint.Payload
		}

		// Write message to connection
		if err := wlc.wsEndpoint.conn.WriteMessage(websocket.TextMessage, payload); err != nil {
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
			return fmt.Errorf("failed to write message: %w", err)
		}
	}

	return nil
}

// wsShortConn is an implementation of taskman.Task that sets up a short-lived WebSocket connection
// to send a message to the endpoint. This is useful for endpoints that require a new connection
// for each message, or for situations where there is no way to link the response to the request.
type wsShortConn struct {
	wsEndpoint *WSEndpoint
}

// Execute sets up a WebSocket connection to the WebSocket endpoint, sends a message, and reads
// the response.
// Note: for concurrency safety, the connection's WriteMessage method is used exclusively here.
func (wsc *wsShortConn) Execute() error {
	// The connection should not be open
	if wsc.wsEndpoint.conn != nil {
		return errors.New("connection is already open")
	}

	wsc.wsEndpoint.lock()
	defer wsc.wsEndpoint.unlock()

	select {
	case <-wsc.wsEndpoint.ctx.Done():
		// Endpoint shutting down, do nothing
		return nil
	default:
		// 1. Establish a new connection
		start := time.Now()
		conn, _, err := websocket.DefaultDialer.Dial(wsc.wsEndpoint.URL.String(), wsc.wsEndpoint.Header)
		if err != nil {
			err = fmt.Errorf("failed to dial: %w", err)
			wsc.wsEndpoint.respChan <- errorResponse(err, wsc.wsEndpoint.id, wsc.wsEndpoint.URL)
			return err
		}
		defer conn.Close()
		handshakeTime := time.Since(start)

		// 2. Write message to connection
		if err := conn.WriteMessage(websocket.TextMessage, wsc.wsEndpoint.Payload); err != nil {
			// An error is unexpected, since the connection was just established
			err = fmt.Errorf("failed to write message: %w", err)
			wsc.wsEndpoint.respChan <- errorResponse(err, wsc.wsEndpoint.id, wsc.wsEndpoint.URL)
			return err
		}

		// 3. Read exactly one response
		_, message, err := conn.ReadMessage()
		if err != nil {
			// An error is unexpected, since the connection was just established
			err = fmt.Errorf("failed to read message: %w", err)
			wsc.wsEndpoint.respChan <- errorResponse(err, wsc.wsEndpoint.id, wsc.wsEndpoint.URL)
			return err
		}

		// 4. Create a task response
		taskResponse := NewWSTaskResponse(message)
		taskResponse.latency = handshakeTime
		taskResponse.receivedAt = time.Now()

		// 5. Send the response message on the channel
		wsc.wsEndpoint.respChan <- WatcherResponse{
			WatcherID: wsc.wsEndpoint.id,
			URL:       wsc.wsEndpoint.URL,
			Err:       nil,
			Payload:   taskResponse,
		}

		// 6. Close the connection gracefully
		closeMsg := websocket.FormatCloseMessage(websocket.CloseNormalClosure, "")
		err = conn.WriteControl(websocket.CloseMessage, closeMsg, time.Now().Add(3*time.Second))
		if err != nil {
			// We tried a graceful close, but maybe the connection is already gone
			return fmt.Errorf("failed to write close message: %w", err)
		}

		// 7. Skip waiting for the server's close message, exit function to close the connection
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
