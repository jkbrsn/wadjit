package wadjit

import (
	"bytes"
	"context"
	"errors"
	"io"
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

// TODO: consider if it's feasible to implement subscriptions, e.g. as another "task type"

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
type Watcher struct {
	id            xid.ID
	cadence       time.Duration
	commonHeader  http.Header
	commonPayload []byte

	http    []Endpoint
	wsConns []WSConnection

	httpRespChan chan httpResponse
	wsReadChan   chan wsRead
	doneChan     chan struct{}
}

// Close closes the HTTP watcher.
func (w *Watcher) Close() error {
	// Signal that the watcher is done
	close(w.doneChan)

	// Close all WS connections
	var result *multierror.Error
	for i := range w.wsConns {
		err := w.wsConns[i].Close()
		if err != nil {
			result = multierror.Append(result, err)
		}
	}
	return result.ErrorOrNil()
}

// ID returns the ID of the HTTPWatcher.
func (w *Watcher) ID() xid.ID {
	return w.id
}

// Job returns a taskman.Job that sends HTTP requests to the endpoints of the HTTPWatcher.
func (w *Watcher) Job() taskman.Job {
	tasks := make([]taskman.Task, 0, len(w.http)+len(w.wsConns))
	// Create HTTP requests
	for _, httpTarget := range w.http {
		tasks = append(tasks, httpRequest{
			Method: http.MethodGet,
			URL:    httpTarget.URL,
			Header: w.commonHeader,
			Data:   w.commonPayload,
		})
	}
	// Create WS requests
	for i := range w.wsConns {
		tasks = append(tasks, &wsSend{
			Conn:    &w.wsConns[i],
			Message: w.commonPayload,
		})
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

// Initialize sets up the HTTPWatcher to start listening for responses.
func (w *Watcher) Initialize(responseChan chan WatcherResponse) error {
	var result *multierror.Error
	// If the response channel is nil, the watcher cannot function
	if responseChan == nil {
		result = multierror.Append(result, errors.New("response channel is nil"))
	}

	// Initialize the internal channels
	w.doneChan = make(chan struct{})
	w.httpRespChan = make(chan httpResponse)
	w.wsReadChan = make(chan wsRead)

	// Establish connections to the WS endpoints
	for i := range w.wsConns {
		w.wsConns[i].Lock()
		newConn, _, err := websocket.DefaultDialer.Dial(w.wsConns[i].url.Host, w.commonHeader)
		if err != nil {
			result = multierror.Append(result, err)
		}
		w.wsConns[i].Conn = newConn
		w.wsConns[i].writeChan = make(chan []byte)
		w.wsConns[i].readChan = w.wsReadChan
		w.wsConns[i].Unlock()

		go w.wsConns[i].read()
	}

	// Start the goroutine that forwards responses to the response channel
	go w.forwardResponses(responseChan)

	return result.ErrorOrNil()
}

// forwardResponses listens for responses from the Watcher's requests, and forwards them to
// the responseChan.
func (w *Watcher) forwardResponses(responseChan chan WatcherResponse) {
	for {
		select {
		// TODO: how to handle Err?
		case resp := <-w.httpRespChan:
			response := WatcherResponse{
				WatcherID: w.id,
				Endpoint:  resp.url,
				Err:       nil,
				Data:      nil,
				Body:      resp.resp.Body,
			}
			responseChan <- response
		case wsRead := <-w.wsReadChan:
			response := WatcherResponse{
				WatcherID: w.id,
				Endpoint:  wsRead.url,
				Err:       nil,
				Data:      wsRead.data,
				Body:      nil,
			}
			responseChan <- response
		case <-w.doneChan:
			return
		}
	}
}

// WSConnection represents and handles a WebSocket connection.
type WSConnection struct {
	*websocket.Conn
	sync.Mutex

	url *url.URL

	writeChan chan []byte
	readChan  chan<- wsRead

	ctx context.Context
}

// read reads messages from the WebSocket connection.
// Note: the read pump has exclusive permission to read from the connection.
func (c *WSConnection) read() {
	defer c.ctx.Done()

	for {
		select {
		case <-c.ctx.Done():
			return
		default:
			// TODO: reset read deadlines before reading ???

			// Read message from connection
			_, p, err := c.ReadMessage()
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
			resp := wsRead{
				data: p,
				url:  c.url,
			}
			c.readChan <- resp
		}
	}
}

// HTTP REQUESTS
// TODO: move to a separate file

type httpResponse struct {
	resp *http.Response
	url  *url.URL
}

// httpRequest is an implementation of taskman.Task that sends an HTTP request to an endpoint.
type httpRequest struct {
	Header http.Header
	Method string
	URL    *url.URL
	Data   []byte

	respChan chan httpResponse
}

// Execute sends an HTTP request to the endpoint.
func (r httpRequest) Execute() error {
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

// wsRead represents a message read from a WebSocket connection to a specific URL.
type wsRead struct {
	data []byte
	url  *url.URL
}

// wsSend is an implementation to taskman.Task that sends a message to a WebSocket endpoint.
type wsSend struct {
	Conn    *WSConnection
	Message []byte
}

// Execute sends a message to the WebSocket endpoint.
// Note: for concurrency safety, the connection's WriteMessage method is used exclusively here.
func (ws *wsSend) Execute() error {
	ws.Conn.Lock()
	defer ws.Conn.Unlock()

	// TODO: use/set a write deadline ???
	if err := ws.Conn.WriteMessage(websocket.TextMessage, ws.Message); err != nil {
		if websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway) {
			// This is an expected situation, handle gracefully
		} else if strings.Contains(err.Error(), "websocket: close sent") {
			// This is an expected situation, handle gracefully
		} else {
			// This is unexpected
		}

		// TODO: if there was an error, close the connection?

		// TODO: unless handled, this error will be caught in the task manager
		return err
	}

	return nil
}
