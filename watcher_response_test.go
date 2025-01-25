package wadjit

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestHTTPTaskResponse_Data_Success(t *testing.T) {
	server := echoServer()
	defer server.Close()

	payload := []byte("hello world")
	resp, err := http.Post(server.URL, "text/plain", bytes.NewReader(payload))
	require.NoError(t, err)
	require.NotNil(t, resp)

	// Call Data() to read the entire body
	taskResp := NewHTTPTaskResponse(resp)
	data, err := taskResp.Data()

	require.NoError(t, err)
	require.Equal(t, payload, data)

	// Calling Data() again should return the cached data
	data2, err2 := taskResp.Data()
	require.NoError(t, err2)
	require.Equal(t, data, data2)
}

func TestHTTPTaskResponse_Data_AfterReaderFails(t *testing.T) {
	server := echoServer()
	defer server.Close()

	resp, err := http.Get(server.URL)
	require.NoError(t, err)
	require.NotNil(t, resp)

	// First, get the Reader
	taskResp := NewHTTPTaskResponse(resp)
	r, err := taskResp.Reader()
	require.NoError(t, err)
	require.NotNil(t, r)
	defer r.Close()

	// Then attempt Data()
	data, err := taskResp.Data()
	require.Nil(t, data)
	require.Error(t, err)
	require.Contains(t, err.Error(), "cannot call Data() after Reader() was already used")
}

func TestHTTPTaskResponse_Reader_AfterDataReturnsMemory(t *testing.T) {
	server := echoServer()
	defer server.Close()

	payload := []byte("response data")
	resp, err := http.Post(server.URL, "text/plain", bytes.NewReader(payload))
	require.NoError(t, err)
	require.NotNil(t, resp)

	// Call Data() first
	taskResp := NewHTTPTaskResponse(resp)
	data, err := taskResp.Data()
	require.NoError(t, err)
	require.Equal(t, payload, data)

	// Then call Reader(). Expect an in-memory stream
	r, err := taskResp.Reader()
	require.NoError(t, err)
	require.NotNil(t, r)

	// Read the response and close the reader
	bytes, err := io.ReadAll(r)
	require.NoError(t, err)
	r.Close()
	require.Equal(t, payload, bytes)
}

func TestHTTPTaskResponse_NilBody(t *testing.T) {
	// Construct a fake response with no body
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body:       nil,
	}

	taskResp := NewHTTPTaskResponse(resp)

	// Data() should return error
	data, err := taskResp.Data()
	require.Nil(t, data)
	require.Error(t, err)

	// Reader() should also fail
	r, err2 := taskResp.Reader()
	require.Nil(t, r)
	require.Error(t, err2)
}

func TestHTTPTaskResponse_Metadata(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Custom-Header", "Value123")
		w.WriteHeader(http.StatusTeapot)
		w.Write([]byte("test"))
	}))
	defer server.Close()

	resp, err := http.Get(server.URL)
	require.NoError(t, err)
	require.NotNil(t, resp)
	taskResp := NewHTTPTaskResponse(resp)

	md := taskResp.Metadata()
	require.Equal(t, http.StatusTeapot, md.StatusCode)
	require.Equal(t, "Value123", md.Headers.Get("X-Custom-Header"))
}

func TestWSTaskResponse_DataAndReader(t *testing.T) {
	wsData := []byte("hello from websocket")
	wsResp := NewWSTaskResponse(wsData)

	data, err := wsResp.Data()
	require.NoError(t, err)
	require.Equal(t, wsData, data)

	// Test Reader
	r, err := wsResp.Reader()
	require.NoError(t, err)

	readBytes, err := io.ReadAll(r)
	require.NoError(t, err)
	r.Close()
	require.Equal(t, wsData, readBytes)

	// Metadata is empty
	md := wsResp.Metadata()
	require.Equal(t, 0, md.StatusCode)
	require.Len(t, md.Headers, 0)
}
