package wadjit

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"maps"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

//
// WatcherResponse
//

// WatcherResponse represents a response from a watcher.
type WatcherResponse struct {
	TaskID    string       // ID of the task that generated the response
	WatcherID string       // ID of the watcher that generated the response
	URL       *url.URL     // URL of the response's target
	Err       error        // Error during the request, nil if the request was successful
	Payload   TaskResponse // Payload stores the response data from the endpoint
}

// Data reads and returns the data from the response.
func (wr WatcherResponse) Data() ([]byte, error) {
	// Check for errors
	if wr.Err != nil {
		return nil, wr.Err
	}
	// Check for missing payload
	if wr.Payload == nil {
		return nil, errors.New("no payload")
	}
	return wr.Payload.Data()
}

// Metadata returns the metadata of the response.
func (wr WatcherResponse) Metadata() TaskResponseMetadata {
	if wr.Payload == nil {
		return TaskResponseMetadata{}
	}
	return wr.Payload.Metadata()
}

// Reader returns a reader for the response data. This is the preferred method to read the response
// if large responses are expected.
func (wr WatcherResponse) Reader() (io.ReadCloser, error) {
	if wr.Err != nil {
		return nil, wr.Err
	}
	if wr.Payload == nil {
		return nil, errors.New("no payload")
	}
	return wr.Payload.Reader()
}

//
// TaskResponse
//

// TaskResponse is a common interface for different types of responses (HTTP or WebSocket).
type TaskResponse interface {
	// Close closes the underlying source, e.g. the HTTP response body.
	Close() error

	// Data reads the entire payload into memory *exactly once*. Closes the underlying source.
	// Subsequent calls either return the cached data.
	Data() ([]byte, error)

	// Reader returns an io.ReadCloser for streaming the data without loading it all into memory.
	// The caller is responsible for closing it. If Data() has already been called, this returns
	// an in-memory reader. The caller is responsible for closing it.
	Reader() (io.ReadCloser, error)

	// Metadata returns metadata connected to the response.
	Metadata() TaskResponseMetadata
}

// TaskResponseMetadata is optional metadata that HTTP or WS might provide.
type TaskResponseMetadata struct {
	RemoteAddr net.Addr

	// HTTP metadata
	StatusCode int
	Headers    http.Header

	// Size is the size of the response body, or message, in bytes.
	Size int64

	// TimeData contains the timing information for the request.
	TimeData RequestTimes

	// DNS contains the DNS decision metadata for the request, when available.
	DNS *DNSMetadata
}

// DNSMetadata describes the DNS resolution data tied to a request.
type DNSMetadata struct {
	Mode          DNSRefreshMode
	TTL           time.Duration
	ExpiresAt     time.Time
	ResolvedAddrs []netip.Addr
	// LookupDuration is the duration taken by the policy-managed DNS lookup, when applicable.
	LookupDuration     time.Duration
	GuardRailTriggered bool
}

// String returns a string representation of the metadata.
//
// revive:disable:add-constant exception
func (m TaskResponseMetadata) String() string {
	var headerParts []string
	for key, values := range m.Headers {
		for _, value := range values {
			headerParts = append(headerParts, key+": "+value)
		}
	}
	headersStr := "{" + strings.Join(headerParts, ", ") + "}"

	dnsStr := "<nil>"
	if m.DNS != nil {
		var addrStrs []string
		for _, addr := range m.DNS.ResolvedAddrs {
			addrStrs = append(addrStrs, addr.String())
		}
		dnsStr = fmt.Sprintf("{Mode: %d, TTL: %s, ExpiresAt: %s, Addrs: [%s], GuardRail: %t}",
			m.DNS.Mode, m.DNS.TTL, m.DNS.ExpiresAt, strings.Join(addrStrs, ", "),
			m.DNS.GuardRailTriggered)
	}

	return fmt.Sprintf("TaskResponseMetadata{StatusCode: %d%sHeaders: %s%sSize: "+
		"%d%sLatency: %s%sTimeSent: %s%sTimeReceived: %s%sDNS: %s}",
		m.StatusCode, ", ",
		headersStr, ", ",
		m.Size, ", ",
		m.TimeData.Latency.String(), ", ",
		m.TimeData.SentAt.String(), ", ",
		m.TimeData.ReceivedAt.String(), ", ",
		dnsStr)
}

// revive:enable:add-constant

//
// HTTP
//

// httpTaskResponse is a TaskResponse for HTTP responses.
type httpTaskResponse struct {
	remoteAddr net.Addr
	resp       *http.Response

	once     sync.Once   // ensures Data() is only processed once
	dataOnce atomic.Bool // new flag to track if once was done
	data     []byte
	dataErr  error

	timestamps requestTimestamps

	dnsDecision *DNSDecision

	usedReader atomic.Bool // flags if we returned a Reader
}

// dataDone checks if the sync.Once for Data() has run.
func (h *httpTaskResponse) dataDone() bool {
	return h.dataOnce.Load()
}

// readBody reads the HTTP response body into memory exactly once and then closes the body.
func (h *httpTaskResponse) readBody() {
	if h.resp.Body == nil {
		h.dataErr = errors.New("http.Response.Body is nil")
		return
	}
	defer func() {
		_ = h.resp.Body.Close()
	}()

	bodyBytes, err := io.ReadAll(h.resp.Body)
	if err != nil {
		// Best-effort drain to EOF to maximize connection reuse.
		// Ignore drain errors because we're about to close anyway.
		_, _ = io.Copy(io.Discard, h.resp.Body)
		h.dataErr = err
		return
	}
	h.data = bodyBytes
	h.dataOnce.Store(true)
	h.timestamps.dataDone = time.Now()
}

// Close closes the HTTP response body.
func (h *httpTaskResponse) Close() error {
	if h.resp.Body == nil {
		return nil
	}
	return h.resp.Body.Close()
}

// Data reads the entire HTTP response body into memory exactly once and then closes the body.
// Further calls return the data from memory.
func (h *httpTaskResponse) Data() ([]byte, error) {
	// If the user already called Reader(), we disallow Data().
	if h.usedReader.Load() {
		return nil, errors.New("cannot call Data() after Reader() was already used")
	}

	h.once.Do(func() {
		h.readBody()
	})

	return h.data, h.dataErr
}

// Reader returns an io.ReadCloser for streaming. If Data() has already been called, we return
// an in-memory buffer to stream from instead.
// Note: the caller is responsible for closing the reader. Closing is a no-op if Data() was called.
func (h *httpTaskResponse) Reader() (io.ReadCloser, error) {
	// If Data() was invoked, disallow reading from the raw body.
	if h.dataDone() {
		if h.dataErr != nil {
			return nil, h.dataErr
		}

		return io.NopCloser(bytes.NewReader(h.data)), nil
	}

	// If Reader() was already called, disallow retrieving the reader again.
	if h.usedReader.Load() {
		return nil, errors.New("reader already called")
	}

	// Otherwise, mark reader as used and return the original body.
	h.usedReader.Store(true)

	if h.resp.Body == nil {
		return nil, errors.New("http.Response.Body is nil")
	}

	// Return custom readcloser that records when the stream is finished
	return &timedReadCloser{
		rc: h.resp.Body,
		doneFn: func() {
			// Only set the timestamp if it hasn't been set yet
			if h.timestamps.dataDone.IsZero() {
				h.timestamps.dataDone = time.Now()
			}
		},
	}, nil
}

// Metadata returns the HTTP status code and headers.
func (h *httpTaskResponse) Metadata() TaskResponseMetadata {
	if h.resp == nil {
		return TaskResponseMetadata{}
	}

	md := TaskResponseMetadata{
		RemoteAddr: h.remoteAddr,
		StatusCode: h.resp.StatusCode,
		Headers:    http.Header{},
		Size:       h.resp.ContentLength,
		TimeData:   TimeDataFromTimestamps(h.timestamps),
	}
	maps.Copy(md.Headers, h.resp.Header)

	if h.dnsDecision != nil {
		decision := *h.dnsDecision
		md.DNS = &DNSMetadata{
			Mode:               decision.Mode,
			TTL:                decision.TTL,
			ExpiresAt:          decision.ExpiresAt,
			ResolvedAddrs:      append([]netip.Addr(nil), decision.ResolvedAddrs...),
			LookupDuration:     decision.LookupDuration,
			GuardRailTriggered: decision.GuardRailTriggered,
		}
		// If httptrace didn't capture DNS timings but the policy did, surface it in TimeData.
		if md.TimeData.DNSLookup == nil && decision.LookupDuration > 0 {
			md.TimeData.DNSLookup = ptr(decision.LookupDuration)
		}
	}

	return md
}

// newHTTPTaskResponse creates a new httpTaskResponse from an http.Response.
func newHTTPTaskResponse(remoteAddr net.Addr, r *http.Response) *httpTaskResponse {
	h := &httpTaskResponse{remoteAddr: remoteAddr, resp: r}
	if r != nil && r.Request != nil {
		if decision, ok := r.Request.Context().Value(dnsDecisionKey{}).(DNSDecision); ok {
			h.dnsDecision = &decision
		}
	}
	return h
}

//
// WebSocket
//

// wsTaskResponse is a TaskResponse for WebSocket responses.
type wsTaskResponse struct {
	remoteAddr net.Addr
	data       []byte
	timestamps requestTimestamps
}

// newWSTaskResponse can store an incoming WS message as a byte slice.
func newWSTaskResponse(remoteAddr net.Addr, data []byte) *wsTaskResponse {
	return &wsTaskResponse{remoteAddr: remoteAddr, data: data}
}

// Close implements the TaskResponse interface but does nothing for WS responses.
func (*wsTaskResponse) Close() error {
	return nil
}

// Data returns the WS message of the response.
func (w *wsTaskResponse) Data() ([]byte, error) {
	return w.data, nil
}

// Reader returns an io.ReadCloser for the data. Closing is a no-op.
func (w *wsTaskResponse) Reader() (io.ReadCloser, error) {
	return io.NopCloser(bytes.NewReader(w.data)), nil
}

// Metadata returns metadata connected to the response.
func (w *wsTaskResponse) Metadata() TaskResponseMetadata {
	return TaskResponseMetadata{
		RemoteAddr: w.remoteAddr,
		Size:       int64(len(w.data)),
		TimeData:   TimeDataFromTimestamps(w.timestamps),
	}
}
