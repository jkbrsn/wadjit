package wadjit

import (
	"bytes"
	"errors"
	"io"
	"maps"
	"net"
	"net/http"
	"net/url"
	"strconv"
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
	Err       error        // Error that occurred during the request, if nil the request was successful
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
	// an in-memory reader.
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
}

func (m TaskResponseMetadata) String() string {
	buffer := bytes.Buffer{}
	buffer.WriteString("{")
	first := true
	for key, values := range m.Headers {
		for _, value := range values {
			if !first {
				buffer.WriteString(", ")
			}
			buffer.WriteString(key + ": " + value)
			first = false
		}
	}
	buffer.WriteString("}")
	return "TaskResponseMetadata{" +
		"StatusCode: " + strconv.Itoa(m.StatusCode) + ", " +
		"Headers: " + buffer.String() + ", " +
		"Size: " + strconv.Itoa(int(m.Size)) + ", " +
		"Latency: " + m.TimeData.Latency.String() + ", " +
		"TimeSent: " + m.TimeData.SentAt.String() + ", " +
		"TimeReceived: " + m.TimeData.ReceivedAt.String() + "}"
}

//
// HTTP
//

// HTTPTaskResponse is a TaskResponse for HTTP responses.
type HTTPTaskResponse struct {
	remoteAddr net.Addr
	resp       *http.Response

	once     sync.Once   // ensures Data() is only processed once
	dataOnce atomic.Bool // new flag to track if once was done
	data     []byte
	dataErr  error

	timestamps requestTimestamps

	usedReader atomic.Bool // flags if we returned a Reader
}

func NewHTTPTaskResponse(remoteAddr net.Addr, r *http.Response) *HTTPTaskResponse {
	return &HTTPTaskResponse{remoteAddr: remoteAddr, resp: r}
}

// readBody reads the HTTP response body into memory exactly once and then closes the body.
func (h *HTTPTaskResponse) readBody() {
	if h.resp.Body == nil {
		h.dataErr = errors.New("http.Response.Body is nil")
		return
	}
	defer h.resp.Body.Close()

	bodyBytes, err := io.ReadAll(h.resp.Body)
	if err != nil {
		h.dataErr = err
		return
	}
	h.data = bodyBytes
	h.dataOnce.Store(true)
	h.timestamps.dataDone = time.Now()
}

// Close closes the HTTP response body.
func (h *HTTPTaskResponse) Close() error {
	if h.resp.Body == nil {
		return nil
	}
	return h.resp.Body.Close()
}

// Data reads the entire HTTP response body into memory exactly once and then closes the body.
// Further calls return the data from memory.
func (h *HTTPTaskResponse) Data() ([]byte, error) {
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
func (h *HTTPTaskResponse) Reader() (io.ReadCloser, error) {
	// If Data() was invoked, disallow reading from the raw body.
	if h.dataDone() {
		if h.dataErr != nil {
			return nil, h.dataErr
		}

		return io.NopCloser(bytes.NewReader(h.data)), nil
	}

	// If Reader() was already called, disallow retrieving the reader again.
	if h.usedReader.Load() {
		return nil, errors.New("Reader() already called")
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
func (h *HTTPTaskResponse) Metadata() TaskResponseMetadata {
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

	return md
}

// dataDone checks if the sync.Once for Data() has run.
func (h *HTTPTaskResponse) dataDone() bool {
	return h.dataOnce.Load()
}

//
// WebSocket
//

// WSTaskResponse is a TaskResponse for WebSocket responses.
type WSTaskResponse struct {
	remoteAddr net.Addr
	data       []byte
	timestamps requestTimestamps
}

// NewWSTaskResponse can store an incoming WS message as a byte slice.
func NewWSTaskResponse(remoteAddr net.Addr, data []byte) *WSTaskResponse {
	return &WSTaskResponse{remoteAddr: remoteAddr, data: data}
}

func (w *WSTaskResponse) Close() error {
	return nil
}

// Data returns the WS message of the response.
func (w *WSTaskResponse) Data() ([]byte, error) {
	return w.data, nil
}

// Reader returns an io.ReadCloser for the data. Closing is a no-op.
func (w *WSTaskResponse) Reader() (io.ReadCloser, error) {
	return io.NopCloser(bytes.NewReader(w.data)), nil
}

// Metadata returns metadata connected to the response.
func (w *WSTaskResponse) Metadata() TaskResponseMetadata {
	return TaskResponseMetadata{
		RemoteAddr: w.remoteAddr,
		Size:       int64(len(w.data)),
		TimeData:   TimeDataFromTimestamps(w.timestamps),
	}
}
