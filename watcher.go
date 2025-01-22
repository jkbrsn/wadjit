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
	"github.com/hashicorp/go-multierror"
	"github.com/jakobilobi/go-taskman"
	"github.com/rs/xid"
)

// Watcher is a watcher that sends HTTP requests and WS messages to endpoints, and then
// forwards the responses to a response channel.
type Watcher struct {
	id      xid.ID
	cadence time.Duration
	payload []byte

	watcherTasks []WatcherTask

	taskResponses chan WatcherResponse
	doneChan      chan struct{}
}

// WatcherResponse represents a response from a watcher.
type WatcherResponse struct {
	WatcherID    xid.ID
	URL          *url.URL
	Err          error
	WSData       []byte         // For WS
	HTTPResponse *http.Response // For HTTP
}

// WatcherTask is a task that the Watcher can execute to interact with a target endpoint.
type WatcherTask interface {
	// Close closes the WatcherTask, cleaning up and releasing resources.
	// Note: will block until the task is closed. (?)
	Close() error

	// Initialize sets up the WatcherTask to be ready to watch an endpoint.
	Initialize(respChan chan<- WatcherResponse) error

	// Task returns a taskman.Task that sends requests and messages to the endpoint.
	Task(payload []byte) taskman.Task

	// Validate checks that the WatcherTask is ready for initialization.
	Validate() error
}

// Close closes the HTTP watcher.
func (w *Watcher) Close() error {
	// Signal that the watcher is done
	close(w.doneChan)

	// Close all WS connections
	var result *multierror.Error
	for i := range w.watcherTasks {
		err := w.watcherTasks[i].Close()
		if err != nil {
			result = multierror.Append(result, err)
		}
	}
	return result.ErrorOrNil()
}

// ID returns the ID of the Watcher.
func (w *Watcher) ID() xid.ID {
	return w.id
}

// Job returns a taskman.Job that executes the Watcher's tasks.
func (w *Watcher) Job() taskman.Job {
	tasks := make([]taskman.Task, 0, len(w.watcherTasks))
	for i := range w.watcherTasks {
		tasks = append(tasks, w.watcherTasks[i].Task(w.payload))
	}
	// Create the job
	job := taskman.Job{
		ID:       w.id.String(),
		Cadence:  w.cadence,
		NextExec: time.Now().Add(w.cadence),
		Tasks:    tasks,
	}
	return job
}

// Start sets up the Watcher to start listening for responses, and initializes its tasks.
func (w *Watcher) Start(responseChan chan WatcherResponse) error {
	var result *multierror.Error

	// If the response channel is nil, the watcher cannot function
	if responseChan == nil {
		result = multierror.Append(result, errors.New("response channel is nil"))
	}

	// Set up the Watcher's channels if they are nil
	if w.doneChan == nil {
		w.doneChan = make(chan struct{})
	}
	if w.taskResponses == nil {
		w.taskResponses = make(chan WatcherResponse) // TODO: make buffered?
	}

	// Initialize the watcher tasks
	for i := range w.watcherTasks {
		err := w.watcherTasks[i].Initialize(w.taskResponses)
		if err != nil {
			result = multierror.Append(result, err)
		}
	}

	// Start the goroutine that forwards responses to the response channel
	go w.forwardResponses(responseChan)

	return result.ErrorOrNil()
}

// Validate checks that the Watcher is valid for use in the Wadjit.
func (w *Watcher) Validate() error {
	if w == nil {
		return errors.New("watcher is nil")
	}

	var result *multierror.Error
	if w.id == xid.NilID() {
		result = multierror.Append(result, errors.New("id must not be nil"))
	}
	if w.cadence <= 0 {
		result = multierror.Append(result, errors.New("cadence must be greater than 0"))
	}
	if len(w.watcherTasks) == 0 {
		result = multierror.Append(result, errors.New("watcherTasks must not be nil or empty"))
	}
	if w.taskResponses == nil {
		result = multierror.Append(result, errors.New("gatherResponses must not be nil"))
	}
	if w.doneChan == nil {
		result = multierror.Append(result, errors.New("doneChan must not be nil"))
	}

	for i := range w.watcherTasks {
		err := w.watcherTasks[i].Validate()
		if err != nil {
			result = multierror.Append(result, err)
		}
	}

	return result.ErrorOrNil()
}

// forwardResponses listens for responses from the Watcher's requests, and forwards them to
// the responseChan.
func (w *Watcher) forwardResponses(responseChan chan WatcherResponse) {
	for {
		select {
		case resp := <-w.taskResponses:
			// Attach Watcher ID to the response
			// TODO: is there some better way to attach the ID than intercepting the response?
			resp.WatcherID = w.id
			responseChan <- resp
		case <-w.doneChan:
			return
		}
	}
}

// NewWatcher creates and validates a new Watcher.
func NewWatcher(
	id xid.ID,
	cadence time.Duration,
	payload []byte,
	tasks []WatcherTask,
	responseChannel chan WatcherResponse,
) (*Watcher, error) {
	w := &Watcher{
		id:            id,
		cadence:       cadence,
		payload:       payload,
		watcherTasks:  tasks,
		taskResponses: make(chan WatcherResponse), // TODO: make buffered?
		doneChan:      make(chan struct{}),
	}

	if err := w.Validate(); err != nil {
		return nil, fmt.Errorf("invalid watcher initialization: %w", err)
	}

	return w, nil
}

// Data returns the data from the response.
func (r WatcherResponse) Data() ([]byte, error) {
	// Check for errors
	if r.Err != nil {
		return nil, r.Err
	}
	// Check for duplicate data, which is undefined behavior
	if r.WSData != nil && r.HTTPResponse != nil {
		return nil, errors.New("both WS and HTTP data available")
	}
	// Check for WS data
	if r.WSData != nil {
		return r.WSData, nil
	}
	// Check for HTTP data
	if r.HTTPResponse != nil {
		// Read the body
		defer r.HTTPResponse.Body.Close()
		buf := new(bytes.Buffer)
		buf.ReadFrom(r.HTTPResponse.Body)
		return buf.Bytes(), nil
	}
	return nil, errors.New("no data available")
}

// errorResponse is a helper to create a WatcherResponse with an error.
func errorResponse(err error, url *url.URL) WatcherResponse {
	return WatcherResponse{
		WatcherID:    xid.NilID(),
		URL:          url,
		Err:          err,
		WSData:       nil,
		HTTPResponse: nil,
	}
}

// HTTPEndpoint represents an HTTP endpoint that the Watcher can interact with.
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

// WSConnection represents and handles a WebSocket connection.
// TODO: add a reconnect mechanism?
// TODO: use read and write deadlines?
// TODO: implement reconnect mechanism
type WSConnection struct {
	mu sync.Mutex

	// Set in initialization
	conn      *websocket.Conn
	ctx       context.Context
	cancel    context.CancelFunc
	writeChan chan []byte
	respChan  chan<- WatcherResponse

	// Set before initialization
	URL    *url.URL
	Header http.Header
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

			// Send the message to the read channel
			response := WatcherResponse{
				WatcherID:    xid.NilID(),
				URL:          c.URL,
				Err:          nil,
				WSData:       p,
				HTTPResponse: nil,
			}
			c.respChan <- response
		}
	}
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

	// Send the response without reading it, leaving that to the Watcher's owner
	r.respChan <- WatcherResponse{
		WatcherID:    xid.NilID(),
		URL:          r.URL,
		Err:          nil,
		WSData:       nil,
		HTTPResponse: response,
	}

	return nil
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
