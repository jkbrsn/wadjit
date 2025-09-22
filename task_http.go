package wadjit

import (
	"bytes"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptrace"
	"net/netip"
	"net/url"
	"time"

	"github.com/jkbrsn/taskman"
	"github.com/rs/xid"
)

const (
	// defaultDialTimeout is the default timeout for network dial operations
	defaultDialTimeout = 5 * time.Second
)

// HTTPEndpointOption is a functional option for the HTTPEndpoint struct.
type HTTPEndpointOption func(*HTTPEndpoint)

// TransportControl contains information about the transport layer of a connection.
type TransportControl struct {
	// A literal address to connect to.
	AddrPort netip.AddrPort
	// Set to true to negotiate a TLS connection after the TCP call.
	TLSEnabled bool
	// SkipTLSVerify disables validation of the server certificate when TLSEnabled is true.
	// Use with caution – intended mainly for tests or trusted internal endpoints.
	SkipTLSVerify bool
}

// HTTPEndpoint spawns tasks to make HTTP requests towards the defined endpoint. Implements the
// WatcherTask interface and is meant for use in a Watcher.
type HTTPEndpoint struct {
	Header  http.Header
	Method  string
	Payload []byte
	URL     *url.URL
	ID      string

	// TransportControl facilitates DNS-bypass when non-nil.
	TransportControl *TransportControl
	client           *http.Client
	dnsPolicy        DNSPolicy
	dnsDecisionHook  DNSDecisionCallback
	dnsMgr           *dnsPolicyManager

	// OptReadFast is a flag that, when set, makes the task execution read the response body into
	// memory and close the body as soon as the full response has been received. This completes the
	// request faster but buffers the body into memory.
	// TODO: consider introducing a max-length option to limit this option for large responses.
	OptReadFast bool

	watcherID string
	respChan  chan<- WatcherResponse
}

// Close closes the HTTP endpoint.
func (*HTTPEndpoint) Close() error {
	return nil
}

// Initialize sets up the HTTP endpoint to be able to send on its responses.
func (e *HTTPEndpoint) Initialize(watcherID string, responseChannel chan<- WatcherResponse) error {
	e.watcherID = watcherID
	e.respChan = responseChannel

	tr := http.DefaultTransport.(*http.Transport).Clone()
	policy := e.dnsPolicy

	if e.TransportControl != nil {
		tc := e.TransportControl
		if policy.Mode != DNSRefreshDefault && policy.Mode != DNSRefreshStatic {
			return fmt.Errorf("transport control incompatible with DNS mode %d", policy.Mode)
		}
		if policy.Mode == DNSRefreshDefault {
			policy.Mode = DNSRefreshStatic
		}
		if (policy.StaticAddr == netip.AddrPort{}) {
			policy.StaticAddr = tc.AddrPort
		}

		if tc.TLSEnabled {
			tr.TLSClientConfig = &tls.Config{
				ServerName:         e.URL.Hostname(),
				InsecureSkipVerify: tc.SkipTLSVerify,
			}
		}
	}

	if err := policy.Validate(); err != nil {
		return err
	}

	mgr := newDNSPolicyManager(policy, e.dnsDecisionHook)
	dialer := &net.Dialer{Timeout: defaultDialTimeout}
	tr.DialContext = mgr.dialContext(dialer)

	e.dnsMgr = mgr
	e.dnsPolicy = policy

	e.client = &http.Client{Transport: &policyTransport{base: tr, mgr: mgr}}

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
	if e.TransportControl != nil {
		if e.TransportControl.AddrPort == (netip.AddrPort{}) {
			return errors.New("TransportControl.AddrPort is empty")
		}
	}
	return e.dnsPolicy.Validate()
}

// WithHeader configures the HTTPEndpoint to use the provided header.
func WithHeader(h http.Header) HTTPEndpointOption {
	return func(ep *HTTPEndpoint) { ep.Header = h }
}

// WithID configures the HTTPEndpoint to use the provided ID.
func WithID(id string) HTTPEndpointOption {
	return func(ep *HTTPEndpoint) { ep.ID = id }
}

// WithPayload configures the HTTPEndpoint to use the provided payload.
func WithPayload(b []byte) HTTPEndpointOption {
	return func(ep *HTTPEndpoint) { ep.Payload = b }
}

// WithReadFast configures the HTTPEndpoint to read the response body into memory and close
// the body as soon as the full response is received.
func WithReadFast() HTTPEndpointOption {
	return func(ep *HTTPEndpoint) { ep.OptReadFast = true }
}

// WithTransportControl configures the HTTPEndpoint to use the provided TransportControl.
func WithTransportControl(tc *TransportControl) HTTPEndpointOption {
	return func(ep *HTTPEndpoint) { ep.TransportControl = tc }
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
	tStore := &traceTimes{}
	remoteAddrChan := make(chan net.Addr, 1)
	trace := traceRequest(tStore, remoteAddrChan)
	ctx := httptrace.WithClientTrace(request.Context(), trace)
	request = request.WithContext(ctx)

	// Add headers to the request
	for key, values := range r.endpoint.Header {
		for _, value := range values {
			request.Header.Add(key, value)
		}
	}

	// Send the request
	response, err := r.endpoint.client.Do(request)
	if err != nil {
		if r.endpoint.dnsMgr != nil {
			r.endpoint.dnsMgr.observeResult(0, err)
		}
		r.respChan <- errorResponse(err, r.endpoint.ID, r.endpoint.watcherID, &urlClone)
		return err
	}
	if r.endpoint.dnsMgr != nil {
		r.endpoint.dnsMgr.observeResult(response.StatusCode, nil)
	}

	var remoteAddr net.Addr
	select {
	case remoteAddr = <-remoteAddrChan:
		// Use the received address
	case <-time.After(100 * time.Millisecond): // Short timeout to avoid indefinite wait
		remoteAddr = nil // Fallback if callback doesn't complete in time
	}

	// Create a task response
	taskResponse := NewHTTPTaskResponse(remoteAddr, response)
	taskResponse.timestamps = tStore.Snapshot()
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
func traceRequest(times *traceTimes, addrChan chan<- net.Addr) *httptrace.ClientTrace {
	return &httptrace.ClientTrace{
		// The earliest guaranteed callback is usually GetConn, so we set the start time there
		GetConn: func(string) { times.start.Store(time.Now()) },
		GotConn: func(info httptrace.GotConnInfo) {
			if info.Conn != nil {
				select {
				case addrChan <- info.Conn.RemoteAddr():
				default: // Non-blocking send to avoid issues if channel is full or closed
				}
			}
		},
		DNSStart:          func(httptrace.DNSStartInfo) { times.dnsStart.Store(time.Now()) },
		DNSDone:           func(httptrace.DNSDoneInfo) { times.dnsDone.Store(time.Now()) },
		ConnectStart:      func(_, _ string) { times.connStart.Store(time.Now()) },
		ConnectDone:       func(_, _ string, _ error) { times.connDone.Store(time.Now()) },
		TLSHandshakeStart: func() { times.tlsStart.Store(time.Now()) },
		TLSHandshakeDone: func(_ tls.ConnectionState, _ error) {
			times.tlsDone.Store(time.Now())
		},
		WroteRequest: func(httptrace.WroteRequestInfo) {
			times.wroteDone.Store(time.Now())
		},
		GotFirstResponseByte: func() { times.firstByte.Store(time.Now()) },
	}
}

// NewHTTPEndpoint creates a new HTTPEndpoint with the given attributes.
func NewHTTPEndpoint(
	u *url.URL,
	method string,
	opts ...HTTPEndpointOption,
) *HTTPEndpoint {
	ep := &HTTPEndpoint{
		URL:              u,
		Method:           method,
		Header:           make(http.Header),
		ID:               xid.New().String(),
		TransportControl: nil,
	}

	for _, opt := range opts {
		opt(ep)
	}

	return ep
}
