package wadjit

import (
	"errors"
	"io"
	"sync"
	"time"
)

// requestTimestamps stores the timestamps of a request's phases.
type requestTimestamps struct {
	start     time.Time
	dnsStart  time.Time
	dnsDone   time.Time
	connStart time.Time
	connDone  time.Time
	tlsStart  time.Time
	tlsDone   time.Time
	wroteDone time.Time
	firstByte time.Time
	dataDone  time.Time
}

// RequestTimes represents the timing information for a layer 7 request.
type RequestTimes struct {
	// Time when the request was sent
	SentAt time.Time
	// Time when the first byte of the response was received
	ReceivedAt time.Time

	// Latency is the time it took to receive the very first byte of the response.
	// For HTTP, this is the time from request send to receiving the first byte of the response.
	// For WS, this is the time from inital Dial to the first 101 response for a new conn, or
	// the time from sending a message to receiving a response on an existing connection.
	Latency time.Duration

	// Optional durations, nil when not applicable
	RequestTimeTotal *time.Duration // Total time taken for a request, including full data transfer
	DNSLookup        *time.Duration // DNS lookup duration
	TCPConnect       *time.Duration // TCP connection duration
	TLSHandshake     *time.Duration // TLS handshake duration
	ServerProcessing *time.Duration // Server processing duration
	DataTransfer     *time.Duration // Data transfer duration
}

// ptr returns a pointer to the given value.
func ptr[T any](v T) *T { return &v }

// TimeDataFromTimestamps returns the RequestTimes from the given requestTimestamps. Calculates and sets
// all durations and times except RequestTimeTotal and DataTransfer.
func TimeDataFromTimestamps(t requestTimestamps) RequestTimes {
	req := RequestTimes{}

	req.SentAt = t.start
	req.ReceivedAt = t.firstByte

	if !t.start.IsZero() && !t.firstByte.IsZero() {
		req.Latency = t.firstByte.Sub(t.start)
	}
	if !t.dnsStart.IsZero() && !t.dnsDone.IsZero() {
		req.DNSLookup = ptr(t.dnsDone.Sub(t.dnsStart))
	}
	if !t.connStart.IsZero() && !t.connDone.IsZero() {
		req.TCPConnect = ptr(t.connDone.Sub(t.connStart))
	}
	if !t.tlsStart.IsZero() && !t.tlsDone.IsZero() {
		req.TLSHandshake = ptr(t.tlsDone.Sub(t.tlsStart))
	}
	if !t.wroteDone.IsZero() && !t.firstByte.IsZero() {
		req.ServerProcessing = ptr(t.firstByte.Sub(t.wroteDone))
	}
	if !t.dataDone.IsZero() && !t.firstByte.IsZero() {
		req.DataTransfer = ptr(t.dataDone.Sub(t.firstByte))
	}
	if !t.dataDone.IsZero() && !t.start.IsZero() {
		req.RequestTimeTotal = ptr(t.dataDone.Sub(t.start))
	}

	return req
}

// timedReadCloser wraps an underlying io.ReadCloser and records when the caller
// finishes reading (EOF) or explicitly closes the stream.
type timedReadCloser struct {
	rc     io.ReadCloser
	doneFn func() // called exactly once when stream is finished
	once   sync.Once
}

// Read reads from the underlying reader and records when the stream is finished.
func (t *timedReadCloser) Read(p []byte) (int, error) {
	n, err := t.rc.Read(p)
	if errors.Is(err, io.EOF) {
		t.once.Do(t.doneFn)
	}
	return n, err
}

// Close closes the underlying reader and records the time when the stream is finished.
func (t *timedReadCloser) Close() error {
	t.once.Do(t.doneFn)
	return t.rc.Close()
}
