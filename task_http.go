package wadjit

import (
	"bytes"
	"crypto/tls"
	"errors"
	"net"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"time"

	"github.com/jkbrsn/go-taskman"
	"github.com/rs/xid"
)

// HTTPEndpoint spawns tasks to make HTTP requests towards the defined endpoint. Implements the
// WatcherTask interface and is meant for use in a Watcher.
type HTTPEndpoint struct {
	Header  http.Header
	Method  string
	Payload []byte
	URL     *url.URL
	ID      string

	// OptReadFast is a flag that, when set, makes the task execution read the response body into
	// memory and close the connection as soon as the response is received. This completes the
	// request faster but buffers the body into memory.
	// TODO: consider introducing a max-length option to limit this option for large responses.
	OptReadFast bool

	// Set by Initialize
	watcherID string
	respChan  chan<- WatcherResponse
}

// Close closes the HTTP endpoint.
func (e *HTTPEndpoint) Close() error {
	return nil
}

// Initialize sets up the HTTP endpoint to be able to send on its responses.
func (e *HTTPEndpoint) Initialize(watcherID string, responseChannel chan<- WatcherResponse) error {
	e.watcherID = watcherID
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
		method:   e.Method,
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
	if e.ID == "" {
		// Set random ID if nil
		e.ID = xid.New().String()
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
	// Clone the URL to avoid downstream mutation
	urlClone := *r.endpoint.URL

	request, err := http.NewRequest(r.method, urlClone.String(), bytes.NewReader(r.data))
	if err != nil {
		r.respChan <- errorResponse(err, r.endpoint.ID, r.endpoint.watcherID, &urlClone)
		return err
	}

	// Add tracing to the request
	timestamps := &requestTimestamps{}
	var remoteAddr net.Addr
	trace := traceRequest(timestamps, &remoteAddr)
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
		r.respChan <- errorResponse(err, r.endpoint.ID, r.endpoint.watcherID, &urlClone)
		return err
	}

	// Create a task response
	taskResponse := NewHTTPTaskResponse(remoteAddr, response)
	taskResponse.timestamps = *timestamps
	if r.endpoint.OptReadFast {
		taskResponse.readBody()
	}

	// Send the response on the channel
	r.respChan <- WatcherResponse{
		TaskID:    r.endpoint.ID,
		WatcherID: r.endpoint.watcherID,
		URL:       &urlClone,
		Err:       nil,
		Payload:   taskResponse,
	}

	return nil
}

// traceRequest traces the HTTP request and stores the timestamps in the provided times.
func traceRequest(times *requestTimestamps, addr *net.Addr) *httptrace.ClientTrace {
	return &httptrace.ClientTrace{
		// The earliest guaranteed callback is usually GetConn, so we set the start time there
		GetConn: func(string) { times.start = time.Now() },
		GotConn: func(info httptrace.GotConnInfo) {
			if info.Conn != nil && addr != nil {
				*addr = info.Conn.RemoteAddr()
			}
		},
		DNSStart:             func(httptrace.DNSStartInfo) { times.dnsStart = time.Now() },
		DNSDone:              func(httptrace.DNSDoneInfo) { times.dnsDone = time.Now() },
		ConnectStart:         func(_, _ string) { times.connStart = time.Now() },
		ConnectDone:          func(_, _ string, _ error) { times.connDone = time.Now() },
		TLSHandshakeStart:    func() { times.tlsStart = time.Now() },
		TLSHandshakeDone:     func(_ tls.ConnectionState, _ error) { times.tlsDone = time.Now() },
		WroteRequest:         func(httptrace.WroteRequestInfo) { times.wroteDone = time.Now() },
		GotFirstResponseByte: func() { times.firstByte = time.Now() },
	}
}

// NewHTTPEndpoint creates a new HTTPEndpoint with the given attributes.
func NewHTTPEndpoint(
	url *url.URL,
	method string,
	header http.Header,
	payload []byte,
	id string,
) *HTTPEndpoint {
	return &HTTPEndpoint{
		Header:  header,
		Method:  method,
		Payload: payload,
		URL:     url,
		ID:      id,
	}
}
