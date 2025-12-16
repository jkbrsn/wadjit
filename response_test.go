package wadjit

import (
	"bytes"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestHTTPTaskResponse_Close(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(echoHandler))
	defer server.Close()

	resp, err := http.Get(server.URL)
	require.NoError(t, err)
	require.NotNil(t, resp)

	taskResp := newHTTPTaskResponse(nil, resp, 0)
	require.NoError(t, taskResp.Close())
}

func TestHTTPTaskResponse_Scenarios(t *testing.T) {
	testCases := []struct {
		name string
		run  func(t *testing.T)
	}{
		{
			name: "data and reader reuse",
			run: func(t *testing.T) {
				server := httptest.NewServer(http.HandlerFunc(echoHandler))
				defer server.Close()

				payload := []byte("hello world")
				resp, err := http.Post(server.URL, "text/plain", bytes.NewReader(payload))
				require.NoError(t, err)
				require.NotNil(t, resp)

				taskResp := newHTTPTaskResponse(server.Listener.Addr(), resp, 0)
				defer func() {
					require.NoError(t, taskResp.Close())
				}()

				data, err := taskResp.Data()
				require.NoError(t, err)
				require.Equal(t, payload, data)

				dataCached, err := taskResp.Data()
				require.NoError(t, err)
				require.Equal(t, payload, dataCached)

				r, err := taskResp.Reader()
				require.NoError(t, err)
				require.NotNil(t, r)
				readData, err := io.ReadAll(r)
				require.NoError(t, err)
				require.NoError(t, r.Close())
				require.Equal(t, payload, readData)

				md := taskResp.Metadata()
				require.Equal(t, http.StatusOK, md.StatusCode)
				require.Equal(t, "text/plain", md.Headers.Get("Content-Type"))
				require.Equal(t, int64(len(payload)), md.Size)
				require.Equal(t, server.Listener.Addr(), md.RemoteAddr)
			},
		},
		{
			name: "reader before data errors",
			run: func(t *testing.T) {
				server := httptest.NewServer(http.HandlerFunc(echoHandler))
				defer server.Close()

				resp, err := http.Get(server.URL)
				require.NoError(t, err)
				require.NotNil(t, resp)

				taskResp := newHTTPTaskResponse(nil, resp, 0)
				defer func() {
					require.NoError(t, taskResp.Close())
				}()

				r, err := taskResp.Reader()
				require.NoError(t, err)
				require.NotNil(t, r)
				defer func() {
					require.NoError(t, r.Close())
				}()

				data, err := taskResp.Data()
				require.Nil(t, data)
				require.Error(t, err)
				require.Contains(t,
					err.Error(), "cannot call Data() after Reader() was already used")
			},
		},
		{
			name: "nil body",
			run: func(t *testing.T) {
				resp := &http.Response{
					StatusCode: http.StatusOK,
					Body:       nil,
				}

				taskResp := newHTTPTaskResponse(nil, resp, 0)
				defer func() {
					require.NoError(t, taskResp.Close())
				}()

				data, err := taskResp.Data()
				require.Nil(t, data)
				require.Error(t, err)

				r, err := taskResp.Reader()
				require.Nil(t, r)
				require.Error(t, err)
			},
		},
		{
			name: "metadata headers",
			run: func(t *testing.T) {
				server := httptest.NewServer(
					http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
						w.Header().Set("X-Custom-Header", "Value123")
						w.WriteHeader(http.StatusTeapot)
						_, _ = w.Write([]byte("test"))
					}))
				defer server.Close()

				resp, err := http.Get(server.URL)
				require.NoError(t, err)
				require.NotNil(t, resp)

				taskResp := newHTTPTaskResponse(nil, resp, 0)
				defer func() {
					require.NoError(t, taskResp.Close())
				}()

				md := taskResp.Metadata()
				require.Equal(t, http.StatusTeapot, md.StatusCode)
				require.Equal(t, "Value123", md.Headers.Get("X-Custom-Header"))
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, tc.run)
	}
}

func TestHTTPTaskResponse_Truncation(t *testing.T) {
	testCases := []struct {
		name              string
		bodySize          int
		maxResponseBytes  int64
		expectTruncated   bool
		expectedDataSize  int
	}{
		{
			name:              "no limit",
			bodySize:          1000,
			maxResponseBytes:  0,
			expectTruncated:   false,
			expectedDataSize:  1000,
		},
		{
			name:              "under limit",
			bodySize:          500,
			maxResponseBytes:  1000,
			expectTruncated:   false,
			expectedDataSize:  500,
		},
		{
			name:              "at limit",
			bodySize:          1000,
			maxResponseBytes:  1000,
			expectTruncated:   false,
			expectedDataSize:  1000,
		},
		{
			name:              "over limit",
			bodySize:          1500,
			maxResponseBytes:  1000,
			expectTruncated:   true,
			expectedDataSize:  1000,
		},
		{
			name:              "way over limit",
			bodySize:          10000,
			maxResponseBytes:  100,
			expectTruncated:   true,
			expectedDataSize:  100,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Create a server that returns a body of the specified size
			body := bytes.Repeat([]byte("x"), tc.bodySize)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write(body)
			}))
			defer server.Close()

			resp, err := http.Get(server.URL)
			require.NoError(t, err)
			require.NotNil(t, resp)

			taskResp := newHTTPTaskResponse(nil, resp, tc.maxResponseBytes)

			// Read the data
			data, err := taskResp.Data()
			require.NoError(t, err)
			require.Len(t, data, tc.expectedDataSize)

			// Check truncation flag
			md := taskResp.Metadata()
			require.Equal(t, tc.expectTruncated, md.Truncated)
			require.Equal(t, int64(tc.expectedDataSize), md.Size)
		})
	}
}

func TestWatcherResponse_PrepareForMetrics_AutoReadsHTTP(t *testing.T) {
	body := bytes.Repeat([]byte("a"), 256)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(body)
	}))
	defer server.Close()

	resp, err := http.Get(server.URL)
	require.NoError(t, err)

	htr := newHTTPTaskResponse(nil, resp, 0)
	wr := &WatcherResponse{WatcherID: "w1", TaskID: "t1", Payload: htr}

	// Seed timestamps so RequestTimeTotal can be computed
	start := time.Now()
	htr.timestamps.start = start
	htr.timestamps.firstByte = start.Add(2 * time.Millisecond)

	mdBefore := wr.Metadata()
	require.Equal(t, int64(len(body)), mdBefore.Size) // ContentLength is known
	require.Nil(t, mdBefore.TimeData.RequestTimeTotal)

	wr.prepareForMetrics()

	md := wr.Metadata()
	require.Equal(t, int64(len(body)), md.Size)
	require.NotNil(t, md.TimeData.RequestTimeTotal)
}

func TestWatcherResponse_PrepareForMetrics_RespectsReaderUse(t *testing.T) {
	body := bytes.Repeat([]byte("b"), 128)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(body)
	}))
	defer server.Close()

	resp, err := http.Get(server.URL)
	require.NoError(t, err)

	htr := newHTTPTaskResponse(nil, resp, 0)
	wr := &WatcherResponse{WatcherID: "w1", TaskID: "t1", Payload: htr}

	// Caller grabs reader first
	r, err := htr.Reader()
	require.NoError(t, err)
	_, _ = io.ReadAll(r)
	_ = r.Close()

	wr.prepareForMetrics()

	md := wr.Metadata()
	require.Equal(t, int64(len(body)), md.Size)
}

func TestHTTPTaskResponse_ReaderWithTruncation(t *testing.T) {
	testCases := []struct {
		name              string
		bodySize          int
		maxResponseBytes  int64
		expectTruncated   bool
		expectedReadSize  int
	}{
		{
			name:              "no limit via reader",
			bodySize:          1000,
			maxResponseBytes:  0,
			expectTruncated:   false,
			expectedReadSize:  1000,
		},
		{
			name:              "under limit via reader",
			bodySize:          500,
			maxResponseBytes:  1000,
			expectTruncated:   false,
			expectedReadSize:  500,
		},
		{
			name:              "at limit via reader",
			bodySize:          1000,
			maxResponseBytes:  1000,
			expectTruncated:   false,
			expectedReadSize:  1000,
		},
		{
			name:              "over limit via reader",
			bodySize:          1500,
			maxResponseBytes:  1000,
			expectTruncated:   true,
			expectedReadSize:  1000,
		},
		{
			name:              "way over limit via reader",
			bodySize:          10000,
			maxResponseBytes:  100,
			expectTruncated:   true,
			expectedReadSize:  100,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Create a server that returns a body of the specified size
			body := bytes.Repeat([]byte("x"), tc.bodySize)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write(body)
			}))
			defer server.Close()

			resp, err := http.Get(server.URL)
			require.NoError(t, err)
			require.NotNil(t, resp)

			taskResp := newHTTPTaskResponse(nil, resp, tc.maxResponseBytes)

			// Get the reader
			reader, err := taskResp.Reader()
			require.NoError(t, err)
			require.NotNil(t, reader)
			defer func() { _ = reader.Close() }()

			// Read all data from the reader
			data, err := io.ReadAll(reader)
			require.NoError(t, err)
			require.Len(t, data, tc.expectedReadSize)

			// Check truncation flag in metadata
			md := taskResp.Metadata()
			require.Equal(t, tc.expectTruncated, md.Truncated)
			require.Equal(t, int64(tc.expectedReadSize), md.Size)
		})
	}
}

func TestWSTaskResponse_DataAndReader(t *testing.T) {
	remoteAddr := net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 1234}
	wsData := []byte("hello from websocket")
	wsResp := newWSTaskResponse(&remoteAddr, wsData)
	defer func() { _ = wsResp.Close() }()

	data, err := wsResp.Data()
	require.NoError(t, err)
	require.Equal(t, wsData, data)

	// Test Reader
	r, err := wsResp.Reader()
	require.NoError(t, err)

	readBytes, err := io.ReadAll(r)
	require.NoError(t, err)
	_ = r.Close()
	require.Equal(t, wsData, readBytes)

	// Metadata is empty
	md := wsResp.Metadata()
	require.Equal(t, 0, md.StatusCode)
	require.Len(t, md.Headers, 0)
}
