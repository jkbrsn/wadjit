package wadjit

import (
	"net/http"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/rs/xid"
	"github.com/stretchr/testify/assert"
)

func TestHTTPEndpointImplementsWatcherTask(t *testing.T) {
	var _ WatcherTask = &HTTPEndpoint{}
}

func TestWSConnnImplementsWatcherTask(t *testing.T) {
	var _ WatcherTask = &WSEndpoint{}
}

//
// HTTP
//

func TestHTTPEndpointInitialize(t *testing.T) {
	url, _ := url.Parse("http://example.com")
	header := make(http.Header)
	responseChan := make(chan WatcherResponse)

	endpoint := &HTTPEndpoint{
		URL:    url,
		Header: header,
	}

	assert.Equal(t, url, endpoint.URL)
	assert.Equal(t, header, endpoint.Header)
	assert.Nil(t, endpoint.respChan)

	err := endpoint.Initialize(xid.NilID(), responseChan)
	assert.NoError(t, err)
	assert.NotNil(t, endpoint.respChan)
}

func TestHTTPEndpointExecute(t *testing.T) {
	server := echoServer()
	defer server.Close()

	url, err := url.Parse(server.URL)
	assert.NoError(t, err, "failed to parse HTTP URL")
	header := make(http.Header)
	responseChan := make(chan WatcherResponse, 1)

	header.Add("Content-Type", "application/json")

	endpoint := &HTTPEndpoint{
		URL:     url,
		Header:  header,
		Payload: []byte(`{"key":"value"}`),
	}

	err = endpoint.Initialize(xid.NilID(), responseChan)
	assert.NoError(t, err)

	task := endpoint.Task()
	assert.NotNil(t, task)

	go func() {
		err := task.Execute()
		assert.NoError(t, err)
	}()

	select {
	case resp := <-responseChan:
		assert.NotNil(t, resp)
		assert.Equal(t, xid.NilID(), resp.WatcherID)
		assert.Equal(t, url, resp.URL)
		assert.NoError(t, resp.Err)
		assert.NotNil(t, resp.Payload)
		// Check the response metadata
		metadata := resp.Metadata()
		assert.NotNil(t, metadata)
		assert.Equal(t, "application/json", metadata.Headers.Get("Content-Type"))
		assert.Greater(t, metadata.Latency, time.Duration(0))
		// Check the response data
		data, err := resp.Data()
		assert.NoError(t, err)
		assert.JSONEq(t, `{"key":"value"}`, string(data))
	case <-time.After(1 * time.Second):
		t.Fatal("timeout waiting for response")
	}
}

//
// WS
//

func TestWSConnInitialize(t *testing.T) {
	server := echoServer()
	defer server.Close()

	wsURL := "ws" + server.URL[4:] + "/ws"
	url, err := url.Parse(wsURL)
	assert.NoError(t, err, "failed to parse URL")
	header := make(http.Header)
	responseChan := make(chan WatcherResponse)

	t.Run("ModeJSONRPC", func(t *testing.T) {
		conn := &WSEndpoint{
			URL:    url,
			Header: header,
			mode:   ModeJSONRPC,
		}

		assert.Equal(t, url, conn.URL)
		assert.Equal(t, header, conn.Header)
		assert.Nil(t, conn.respChan)

		err = conn.Initialize(xid.NilID(), responseChan)
		assert.NoError(t, err)
		assert.NotNil(t, conn.respChan)
		assert.NotNil(t, conn.conn)
		assert.NotNil(t, conn.ctx)
		assert.NotNil(t, conn.cancel)
	})

	t.Run("ModeText", func(t *testing.T) {
		conn := &WSEndpoint{
			URL:    url,
			Header: header,
			mode:   ModeText,
		}

		assert.Equal(t, url, conn.URL)
		assert.Equal(t, header, conn.Header)
		assert.Nil(t, conn.respChan)

		err = conn.Initialize(xid.NilID(), responseChan)
		assert.NoError(t, err)
		assert.NotNil(t, conn.respChan)
		assert.Nil(t, conn.conn) // no connection should be established since wsShortConn is used
		assert.NotNil(t, conn.ctx)
		assert.NotNil(t, conn.cancel)
	})
}

// TODO: test error case, when the connection fails and we need to reconnect
func TestWSEndpointExecutewsLongConn(t *testing.T) {
	server := jsonRPCServer()
	defer server.Close()

	wsURL := "ws" + server.URL[4:] + "/ws"
	url, err := url.Parse(wsURL)
	assert.NoError(t, err, "failed to parse URL")
	header := make(http.Header)
	responseChan := make(chan WatcherResponse, 2)

	originalID := xid.New().String()
	payload := `{"id":"` + originalID + `","method":"echo","params":["test"],"jsonrpc":"2.0"}`
	endpoint := &WSEndpoint{
		URL:     url,
		Header:  header,
		mode:    ModeJSONRPC,
		Payload: []byte(payload),
	}

	err = endpoint.Initialize(xid.NilID(), responseChan)
	assert.NoError(t, err)

	task := endpoint.Task()
	assert.NotNil(t, task)

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		err := task.Execute()
		assert.NoError(t, err)
		wg.Done()
	}()

	wg.Wait()

	length := 0
	endpoint.inflightMsgs.Range(func(key, value interface{}) bool {
		length++
		return true
	})
	assert.Equal(t, 1, length)
	var inflightMsg WSInflightMessage
	endpoint.inflightMsgs.Range(func(key, value interface{}) bool {
		inflightMsg = value.(WSInflightMessage)
		return false // Stop after the first item
	})
	assert.Equal(t, originalID, inflightMsg.originalID)
	expectedResult := []byte(`{"id":"` + inflightMsg.inflightID + `","method":"echo","params":["test"],"jsonrpc":"2.0"}`)
	resp := JSONRPCResponse{
		id:     originalID,
		Result: expectedResult,
	}
	expectedResp, err := resp.MarshalJSON()
	assert.NoError(t, err)

	select {
	case resp := <-responseChan:
		assert.NotNil(t, resp)
		assert.Equal(t, xid.NilID(), resp.WatcherID)
		assert.Equal(t, url, resp.URL)
		assert.NoError(t, resp.Err)
		assert.NotNil(t, resp.Payload)
		// Check the response metadata
		metadata := resp.Metadata()
		assert.NotNil(t, metadata)
		assert.Nil(t, metadata.Headers)
		assert.Zero(t, metadata.StatusCode)
		assert.Equal(t, len(expectedResp), int(metadata.Size))
		assert.Greater(t, metadata.Latency, time.Duration(0))
		// Check the response data
		data, err := resp.Data()
		assert.NoError(t, err)
		assert.JSONEq(t, string(expectedResp), string(data))
		//assert.JSONEq(t, payload, string(data))
	case <-time.After(1 * time.Second):
		t.Fatal("timeout waiting for response")
	}
}

// TODO: test shortConn + JSONRPC
func TestWSEndpointExecutewsShortConn(t *testing.T) {
	server := echoServer()
	defer server.Close()

	wsURL := "ws" + server.URL[4:] + "/ws"
	url, err := url.Parse(wsURL)
	assert.NoError(t, err, "failed to parse URL")
	header := make(http.Header)
	responseChan := make(chan WatcherResponse)

	endpoint := &WSEndpoint{
		URL:     url,
		Header:  header,
		mode:    ModeText,
		Payload: []byte(`{"key":"value"}`),
	}

	err = endpoint.Initialize(xid.NilID(), responseChan)
	assert.NoError(t, err)

	task := endpoint.Task()
	assert.NotNil(t, task)

	go func() {
		err := task.Execute()
		assert.NoError(t, err)
	}()

	select {
	case resp := <-responseChan:
		assert.NotNil(t, resp)
		assert.Equal(t, xid.NilID(), resp.WatcherID)
		assert.Equal(t, url, resp.URL)
		assert.NoError(t, resp.Err)
		assert.NotNil(t, resp.Payload)
		// Check the response metadata
		metadata := resp.Metadata()
		assert.NotNil(t, metadata)
		assert.Nil(t, metadata.Headers)
		assert.Zero(t, metadata.StatusCode)
		assert.Equal(t, len(endpoint.Payload), int(metadata.Size))
		assert.Greater(t, metadata.Latency, time.Duration(0))
		// Check the response data
		data, err := resp.Data()
		assert.NoError(t, err)
		assert.JSONEq(t, `{"key":"value"}`, string(data))
	case <-time.After(1 * time.Second):
		t.Fatal("timeout waiting for response")
	}
}

func TestWSConnReconnect(t *testing.T) {
	server := echoServer()
	defer server.Close()

	wsURL := "ws" + server.URL[4:] + "/ws"
	url, err := url.Parse(wsURL)
	assert.NoError(t, err, "failed to parse URL")
	header := make(http.Header)
	responseChan := make(chan WatcherResponse)

	conn := &WSEndpoint{
		URL:    url,
		Header: header,
		mode:   ModeJSONRPC,
	}

	err = conn.Initialize(xid.NilID(), responseChan)
	assert.NoError(t, err)

	err = conn.connect()
	assert.Error(t, err)

	err = conn.reconnect()
	assert.NoError(t, err)
}
