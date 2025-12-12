package wadjit

import (
	"errors"
	"io"
	"sync"
	"time"

	"go.uber.org/atomic"
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

// traceTimes stores the timestamps of a request's phases as atomic values, for use in the
// httptrace.ClientTrace. Atomic values are used to avoid race conditions due to the async nature
// of the httptrace.ClientTrace.
type traceTimes struct {
	// TODO: consider exchanging for atomic.Int64 to go allocation free
	start     atomic.Time
	dnsStart  atomic.Time
	dnsDone   atomic.Time
	connStart atomic.Time
	connDone  atomic.Time
	tlsStart  atomic.Time
	tlsDone   atomic.Time
	wroteDone atomic.Time
	firstByte atomic.Time
	dataDone  atomic.Time
}

// RequestTimes represents the timing information for a layer 7 request.
type RequestTimes struct {
	// Time when the request was sent
	SentAt time.Time
	// Time when the first byte of the response was received
	ReceivedAt time.Time

	// Latency is the time it took to receive the very first byte of the response.
	// For HTTP, this is the time from request send to receiving the first byte of the response.
	// For WS, this is the time from initial Dial to the first 101 response for a new conn, or
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

// Snapshot returns a snapshot of the trace times as a requestTimestamps.
func (t *traceTimes) Snapshot() requestTimestamps {
	return requestTimestamps{
		start:     t.start.Load(),
		dnsStart:  t.dnsStart.Load(),
		dnsDone:   t.dnsDone.Load(),
		connStart: t.connStart.Load(),
		connDone:  t.connDone.Load(),
		tlsStart:  t.tlsStart.Load(),
		tlsDone:   t.tlsDone.Load(),
		wroteDone: t.wroteDone.Load(),
		firstByte: t.firstByte.Load(),
		dataDone:  t.dataDone.Load(),
	}
}

// ptr returns a pointer to the given value.
func ptr[T any](v T) *T { return &v }

// TimeDataFromTimestamps returns the RequestTimes from the given requestTimestamps. Calculates and
// sets all durations and times except RequestTimeTotal and DataTransfer.
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

// limitedTimedReadCloser wraps an io.ReadCloser with size limiting and timing.
// It uses io.LimitReader to cap reads at maxBytes+1, allowing detection of truncation.
type limitedTimedReadCloser struct {
	resp      *httpTaskResponse // parent response for setting truncated flag
	reader    io.Reader         // io.LimitReader(body, maxBytes+1)
	totalRead int64             // tracks total bytes read
	eof       bool              // set when we've hit EOF or truncation limit
	doneFn    func()            // called exactly once when stream is finished
	once      sync.Once
	mu        sync.Mutex
}

// Read reads from the underlying limited reader and detects truncation.
func (l *limitedTimedReadCloser) Read(p []byte) (int, error) {
	l.mu.Lock()
	if l.eof {
		l.mu.Unlock()
		return 0, io.EOF
	}
	l.mu.Unlock()

	n, err := l.reader.Read(p)

	l.mu.Lock()
	l.totalRead += int64(n)

	// Check if we exceeded the limit (read more than maxBytes)
	if l.resp.maxResponseBytes > 0 && l.totalRead > l.resp.maxResponseBytes {
		// Calculate how many valid bytes to return (trim excess)
		excess := l.totalRead - l.resp.maxResponseBytes
		actualN := n - int(excess)
		if actualN < 0 {
			actualN = 0
		}

		// Mark as truncated and update reader bytes read (final value at truncation)
		l.resp.truncatedMu.Lock()
		l.resp.truncated = true
		l.resp.readerBytesRead = l.resp.maxResponseBytes
		l.resp.truncatedMu.Unlock()

		l.eof = true
		l.mu.Unlock()
		l.once.Do(l.doneFn)
		return actualN, io.EOF
	}
	l.mu.Unlock()

	if errors.Is(err, io.EOF) {
		// Update reader bytes read (final value at natural EOF)
		l.resp.truncatedMu.Lock()
		l.resp.readerBytesRead = l.totalRead
		l.resp.truncatedMu.Unlock()

		l.mu.Lock()
		l.eof = true
		l.mu.Unlock()
		l.once.Do(l.doneFn)
	}

	return n, err
}

// Close closes the underlying body and records the time when the stream is finished.
func (l *limitedTimedReadCloser) Close() error {
	l.once.Do(l.doneFn)
	return l.resp.resp.Body.Close()
}
