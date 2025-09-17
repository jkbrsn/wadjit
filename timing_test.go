package wadjit

import (
	"bytes"
	"io"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestTimedReadCloser_EOFTriggersDone(t *testing.T) {
	var called int32
	reader := io.NopCloser(bytes.NewBufferString("hello"))
	trc := &timedReadCloser{
		rc: reader,
		doneFn: func() {
			atomic.AddInt32(&called, 1)
		},
	}

	buf := make([]byte, 5)
	n, err := trc.Read(buf)
	require.Equal(t, 5, n)
	require.NoError(t, err)

	n, err = trc.Read(make([]byte, 1))
	require.Zero(t, n)
	require.ErrorIs(t, err, io.EOF)
	require.Equal(t, int32(1), atomic.LoadInt32(&called))

	require.NoError(t, trc.Close())
	require.Equal(t, int32(1), atomic.LoadInt32(&called))
}

func TestTimedReadCloser_CloseTriggersDone(t *testing.T) {
	var called int32
	trc := &timedReadCloser{
		rc: io.NopCloser(bytes.NewBufferString("data")),
		doneFn: func() {
			atomic.AddInt32(&called, 1)
		},
	}

	require.NoError(t, trc.Close())
	require.Equal(t, int32(1), atomic.LoadInt32(&called))

	require.NoError(t, trc.Close())
	require.Equal(t, int32(1), atomic.LoadInt32(&called))
}

func TestTimeDataFromTimestamps(t *testing.T) {
	start := time.Now()
	ts := requestTimestamps{
		start:     start,
		dnsStart:  start.Add(1 * time.Millisecond),
		dnsDone:   start.Add(4 * time.Millisecond),
		connStart: start.Add(4 * time.Millisecond),
		connDone:  start.Add(7 * time.Millisecond),
		tlsStart:  start.Add(7 * time.Millisecond),
		tlsDone:   start.Add(10 * time.Millisecond),
		wroteDone: start.Add(12 * time.Millisecond),
		firstByte: start.Add(18 * time.Millisecond),
		dataDone:  start.Add(28 * time.Millisecond),
	}

	req := TimeDataFromTimestamps(ts)

	require.Equal(t, start, req.SentAt)
	require.Equal(t, ts.firstByte, req.ReceivedAt)
	require.Equal(t, 18*time.Millisecond, req.Latency)

	require.NotNil(t, req.DNSLookup)
	require.Equal(t, 3*time.Millisecond, *req.DNSLookup)
	require.NotNil(t, req.TCPConnect)
	require.Equal(t, 3*time.Millisecond, *req.TCPConnect)
	require.NotNil(t, req.TLSHandshake)
	require.Equal(t, 3*time.Millisecond, *req.TLSHandshake)
	require.NotNil(t, req.ServerProcessing)
	require.Equal(t, 6*time.Millisecond, *req.ServerProcessing)
	require.NotNil(t, req.DataTransfer)
	require.Equal(t, 10*time.Millisecond, *req.DataTransfer)
	require.NotNil(t, req.RequestTimeTotal)
	require.Equal(t, 28*time.Millisecond, *req.RequestTimeTotal)
}
