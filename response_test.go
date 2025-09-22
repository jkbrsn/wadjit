package wadjit

import (
	"bytes"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestHTTPTaskResponse_Close(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(echoHandler))
	defer server.Close()

	resp, err := http.Get(server.URL)
	require.NoError(t, err)
	require.NotNil(t, resp)

	taskResp := newHTTPTaskResponse(nil, resp)
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

				taskResp := newHTTPTaskResponse(server.Listener.Addr(), resp)
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

				taskResp := newHTTPTaskResponse(nil, resp)
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

				taskResp := newHTTPTaskResponse(nil, resp)
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

				taskResp := newHTTPTaskResponse(nil, resp)
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
