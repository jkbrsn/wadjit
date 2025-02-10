package wadjit

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"math/rand"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/bytedance/sonic"
	"github.com/bytedance/sonic/ast"
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
	inflightMsgs sync.Map // Key string to value time.Time
	wg           sync.WaitGroup

	id       xid.ID
	respChan chan<- WatcherResponse
}

// WSEndpointMode is an enum for the mode of the WebSocket endpoint.
type WSEndpointMode int

const (
	ModeUnknown WSEndpointMode = iota // Defaults to ModeText
	ModeText                          // Text mode is the default mode
	ModeJSONRPC                       // JSON RPC mode
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
	case ModeJSONRPC:
		err := e.connect()
		if err != nil {
			return fmt.Errorf("failed to connect when initializing: %w", err)
		}
	case ModeText:
		// Text mode does not require a connection to be established, so do nothing
	default:
		// Default to text mode, since its task does not require anything outside of its own scope
		// TODO: make default detect the proper mode based on analysis of the payload, instead of defaulting to text mode
		e.mu.Lock()
		e.mode = ModeText
		e.mu.Unlock()
	}

	return nil
}

// Task returns a taskman.Task that sends a message to the WebSocket endpoint.
func (e *WSEndpoint) Task() taskman.Task {
	switch e.mode {
	case ModeText:
		return &wsShortConn{
			wsEndpoint: e,
		}
	case ModeJSONRPC:
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
	if e.mode == ModeText {
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
	if e.mode == ModeText {
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
			fmt.Printf("Received response: %v\n", jsonRPCResp)
			if !jsonRPCResp.IsEmpty() {
				responseID, err := jsonRPCResp.ID()
				if err != nil {
					// Send an error response
					e.respChan <- errorResponse(err, e.id, e.URL)
					return
				}
				responseIDStr := fmt.Sprintf("%v", responseID)

				// 3. If the ID is known, get the inflight map metadata and delete the ID in the map
				if inflightMsg, ok := e.inflightMsgs.Load(responseIDStr); ok {
					inflightMsg := inflightMsg.(WSInflightMessage)
					fmt.Printf("Received response for ID %v\n", inflightMsg)
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
		if wlc.wsEndpoint.mode == ModeJSONRPC {
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

// TODO: move all below to separate file

// JSON RPC

// JSONRPCError represents a standard JSON RPC error.
type JSONRPCError struct {
	Code    int    `json:"code,omitempty"`
	Message string `json:"message,omitempty"`

	// Errors might contain additional data, e.g. revert reason
	Data interface{} `json:"data,omitempty"`
}

// JSONRPCRequest is a struct for JSON RPC requests.
type JSONRPCRequest struct {
	JSONRPC string        `json:"jsonrpc,omitempty"`
	ID      interface{}   `json:"id,omitempty"`
	Method  string        `json:"method"`
	Params  []interface{} `json:"params"`
}

// IDString returns the ID as a string, regardless of its type.
func (r *JSONRPCRequest) IDString() string {
	switch id := r.ID.(type) {
	case string:
		return id
	case int64:
		return fmt.Sprintf("%d", id)
	default:
		return ""
	}
}

// IsEmpty returns whether the JSON RPC request can be considered empty.
func (r *JSONRPCRequest) IsEmpty() bool {
	if r == nil {
		return true
	}

	if r.Method == "" {
		return true
	}

	return false
}

// UnmarshalJSON unmarshals a JSON RPC request using sonic. It includes two custom actions:
// - Sets the JSON RPC version to 2.0.
// - Unmarshals the ID seprately, to handle both string and float64 types.
func (r *JSONRPCRequest) UnmarshalJSON(data []byte) error {
	type Alias JSONRPCRequest
	aux := &struct {
		*Alias
		ID json.RawMessage `json:"id,omitempty"`
	}{
		Alias: (*Alias)(r),
	}
	aux.JSONRPC = "2.0"

	if err := sonic.Unmarshal(data, &aux); err != nil {
		return err
	}

	// Unmarshal the ID separately
	if aux.ID != nil {
		var id interface{}
		if err := sonic.Unmarshal(aux.ID, &id); err != nil {
			return err
		}
		switch v := id.(type) {
		case float64:
			r.ID = int64(v)
		case string:
			r.ID = v
		}
	}

	// Set a random ID if none is provided
	if r.ID == nil {
		r.ID = RandomJSONRPCID()
	} else {
		switch id := r.ID.(type) {
		case string:
			if id == "" {
				r.ID = RandomJSONRPCID()
			}
		}
	}

	return nil
}

// JSONRPCResponse is a struct for JSON RPC responses.
type JSONRPCResponse struct {
	id      interface{}
	idBytes []byte
	muID    sync.RWMutex

	Error    *JSONRPCError
	errBytes []byte
	muErr    sync.RWMutex

	Result   []byte
	muResult sync.RWMutex
	astNode  *ast.Node
}

// ID returns the ID of the JSON RPC response.
func (r *JSONRPCResponse) ID() (interface{}, error) {
	r.muID.RLock()

	if r.id != nil {
		r.muID.RUnlock()
		return r.id, nil
	}
	r.muID.RUnlock()

	r.muID.Lock()
	defer r.muID.Unlock()

	if len(r.idBytes) == 0 {
		return nil, errors.New("ID is nil")
	}

	err := sonic.Unmarshal(r.idBytes, &r.id)
	if err != nil {
		return nil, fmt.Errorf("failed to unmarshal ID: %w", err)
	}

	return r.id, nil
}

// IsEmpty returns whether the JSON RPC response can be considered empty.
func (r *JSONRPCResponse) IsEmpty() bool {
	if r == nil {
		return true
	}

	r.muResult.RLock()
	defer r.muResult.RUnlock()

	lnr := len(r.Result)
	if lnr == 0 ||
		(lnr == 4 && r.Result[0] == '"' && r.Result[1] == '0' && r.Result[2] == 'x' && r.Result[3] == '"') ||
		(lnr == 4 && r.Result[0] == 'n' && r.Result[1] == 'u' && r.Result[2] == 'l' && r.Result[3] == 'l') ||
		(lnr == 2 && r.Result[0] == '"' && r.Result[1] == '"') ||
		(lnr == 2 && r.Result[0] == '[' && r.Result[1] == ']') ||
		(lnr == 2 && r.Result[0] == '{' && r.Result[1] == '}') {
		fmt.Print("something is nil")
		return true
	}

	return false
}

// MarshalJSON marshals a JSON RPC response into a byte slice.
func (r *JSONRPCResponse) MarshalJSON() ([]byte, error) {
	r.muID.RLock()
	defer r.muID.RUnlock()
	r.muErr.RLock()
	defer r.muErr.RUnlock()
	r.muResult.RLock()
	defer r.muResult.RUnlock()

	response := map[string]interface{}{
		"id":     r.id,
		"error":  r.Error,
		"result": r.Result,
	}

	return sonic.Marshal(response)
}

// ParseError parses an error from a raw JSON RPC response.
func (r *JSONRPCResponse) ParseError(raw string) error {
	r.muErr.Lock()
	defer r.muErr.Unlock()

	r.errBytes = nil

	// First attempt to unmarshal the error as a typical JSON-RPC error
	var rpcErr JSONRPCError
	if err := sonic.UnmarshalString(raw, &rpcErr); err != nil {
		// Special case: check for non-standard error structures in the raw data
		if raw == "" || raw == "null" {
			r.Error = &JSONRPCError{
				int(-32603), // TODO: ServerSideException, use a custom error code enum
				"unexpected empty response from upstream endpoint",
				"",
			}
			return nil
		}
	}

	// Check if the error is well-formed and has necessary fields
	if rpcErr.Code != 0 || rpcErr.Message != "" {
		r.Error = &rpcErr
		r.errBytes = Str2Mem(raw)
		return nil
	}

	// Handle case: numeric "code", "message", and "data"
	caseNumerics := &struct {
		Code    int    `json:"code,omitempty"`
		Message string `json:"message,omitempty"`
		Data    string `json:"data,omitempty"`
	}{}
	if err := sonic.UnmarshalString(raw, caseNumerics); err == nil {
		if caseNumerics.Code != 0 || caseNumerics.Message != "" || caseNumerics.Data != "" {
			r.Error = &JSONRPCError{
				caseNumerics.Code,
				caseNumerics.Message,
				caseNumerics.Data,
			}

			return nil
		}
	}

	// Handle case: only "error" field as a string
	caseErrorStr := &struct {
		Error string `json:"error"`
	}{}
	if err := sonic.UnmarshalString(raw, caseErrorStr); err == nil && caseErrorStr.Error != "" {
		r.Error = &JSONRPCError{
			int(-32603), // TODO: ServerSideException, use a custom error code enum
			caseErrorStr.Error,
			"",
		}
		return nil
	}

	// Handle case: no match, treat the raw data as message string
	r.Error = &JSONRPCError{
		int(-32603), // TODO: ServerSideException, use a custom error code enum
		raw,
		"",
	}
	return nil
}

// ParseFromStream parses a JSON RPC response from a stream.
func (r *JSONRPCResponse) ParseFromStream(reader io.Reader, expectedSize int) error {
	// 16KB chunks by default
	chunkSize := 16 * 1024
	data, err := ReadAll(reader, int64(chunkSize), expectedSize)
	if err != nil {
		return err
	}

	return r.ParseFromBytes(data, expectedSize)
}

// ParseFromStream parses a JSON RPC response from a byte slice.
func (r *JSONRPCResponse) ParseFromBytes(data []byte, expectedSize int) error {
	// Parse the JSON data into an ast.Node
	searcher := ast.NewSearcher(Mem2Str(data))
	searcher.CopyReturn = false
	searcher.ConcurrentRead = false
	searcher.ValidateJSON = false

	// Extract the "id" field
	if idNode, err := searcher.GetByPath("id"); err == nil {
		if rawID, err := idNode.Raw(); err == nil {
			r.muID.Lock()
			defer r.muID.Unlock()
			r.idBytes = Str2Mem(rawID)
		}
	}

	// Extract the "result" or "error" field
	if resultNode, err := searcher.GetByPath("result"); err == nil {
		if rawResult, err := resultNode.Raw(); err == nil {
			r.muResult.Lock()
			defer r.muResult.Unlock()
			r.Result = Str2Mem(rawResult)
			r.astNode = &resultNode
		} else {
			return err
		}
	} else if errorNode, err := searcher.GetByPath("error"); err == nil {
		if rawError, err := errorNode.Raw(); err == nil {
			if err := r.ParseError(rawError); err != nil {
				return err
			}
		} else {
			return err
		}
	} else if err := r.ParseError(Mem2Str(data)); err != nil {
		return err
	}

	return nil
}

// HELPERS

// RandomJSONRPCID returns a value appropriate for a JSON RPC ID field, e.g. a int64 type but
// with only 32 bits range, to avoid overflow during conversions and reading/sending to upstreams.
func RandomJSONRPCID() int64 {
	return int64(rand.Intn(math.MaxInt32)) // #nosec G404
}

// ReadAll reads all data from the given reader and returns it as a byte slice.
func ReadAll(reader io.Reader, chunkSize int64, expectedSize int) ([]byte, error) {
	// 16KB buffer by default
	buffer := bytes.NewBuffer(make([]byte, 0, 16*1024))

	// TODO: move this size limit to a config setting
	upperSizeLimit := 50 * 1024 * 1024 // 50MB cap to avoid DDoS by a corrupt/malicious upstream
	if expectedSize > 0 && expectedSize < upperSizeLimit {
		n := expectedSize - buffer.Cap()
		if n > 0 {
			buffer.Grow(n)
		}
	}

	// Read data in chunks
	for {
		n, err := io.CopyN(buffer, reader, chunkSize)
		if err != nil {
			if err == io.EOF {
				break
			}
			return nil, err
		}
		if n == 0 {
			break
		}
	}

	return buffer.Bytes(), nil
}

// Mem2Str safely converts a byte slice to a string without copying the underlying data.
// This is a read-only operation, as strings are immutable in Go.
// Note: Avoid modifying the original byte slice after conversion.
func Mem2Str(b []byte) string {
	return string(b)
}

// Str2Mem safely converts a string to a byte slice without copying the underlying data.
// The resulting byte slice should only be used for read operations to avoid violating
// Go's string immutability guarantee.
func Str2Mem(s string) []byte {
	return []byte(s)
}
