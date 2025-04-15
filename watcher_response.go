package wadjit

import (
	"bytes"
	"errors"
	"io"
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
	// HTTP metadata
	StatusCode int
	Headers    http.Header

	// Latency is the time it took to receive the very first byte of the response.
	// For HTTP, this is the time from request send to receiving the first byte of the response.
	// For WS, this is the time from inital Dial to the first 101 response for a new conn, or
	// the time from sending a message to receiving a response on an existing connection.
	Latency time.Duration
	// Size is the size of the response body, or message, in bytes.
	Size int64
	// TimeReceived is the time the response was received.
	TimeReceived time.Time
	// TimeSent is the time the request or message was sent.
	TimeSent time.Time
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
		"Latency: " + m.Latency.String() + ", " +
		"Size: " + strconv.Itoa(int(m.Size)) + ", " +
		"TimeReceived: " + m.TimeReceived.String() + "}"
}

//
// HTTP
//

// HTTPTaskResponse is a TaskResponse for HTTP responses.
type HTTPTaskResponse struct {
	resp *http.Response

	once       sync.Once   // ensures Data() is only processed once
	dataOnce   atomic.Bool // new flag to track if once was done
	data       []byte
	dataErr    error
	latency    time.Duration
	receivedAt time.Time
	sentAt     time.Time

	usedReader atomic.Bool // flags if we returned a Reader
}

func NewHTTPTaskResponse(r *http.Response) *HTTPTaskResponse {
	return &HTTPTaskResponse{resp: r}
}

// Data reads the entire HTTP response body into memory exactly once and then closes the body.
// Further calls return the data from memory.
func (h *HTTPTaskResponse) Data() ([]byte, error) {
	// If the user already called Reader(), we disallow Data().
	if h.usedReader.Load() {
		return nil, errors.New("cannot call Data() after Reader() was already used")
	}

	h.once.Do(func() {
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

	return h.resp.Body, nil
}

// Metadata returns the HTTP status code and headers.
func (h *HTTPTaskResponse) Metadata() TaskResponseMetadata {
	if h.resp == nil {
		return TaskResponseMetadata{}
	}

	md := TaskResponseMetadata{
		StatusCode:   h.resp.StatusCode,
		Headers:      http.Header{},
		Latency:      h.latency,
		Size:         h.resp.ContentLength,
		TimeReceived: h.receivedAt,
		TimeSent:     h.sentAt,
	}
	for k, v := range h.resp.Header {
		md.Headers[k] = v
	}

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
	data       []byte
	latency    time.Duration
	receivedAt time.Time
	sentAt     time.Time
}

// NewWSTaskResponse can store an incoming WS message as a byte slice.
func NewWSTaskResponse(data []byte) *WSTaskResponse {
	return &WSTaskResponse{data: data}
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
		Latency:      w.latency,
		Size:         int64(len(w.data)),
		TimeReceived: w.receivedAt,
		TimeSent:     w.sentAt,
	}
}
