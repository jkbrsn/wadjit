package wadjit

import (
	"net/http"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/rs/xid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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

	endpoint := NewHTTPEndpoint(url, header, nil)

	assert.Equal(t, url, endpoint.URL)
	assert.Equal(t, header, endpoint.Header)
	assert.Nil(t, endpoint.respChan)

	err := endpoint.Initialize("", responseChan)
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

	endpoint := NewHTTPEndpoint(url, header, []byte(`{"key":"value"}`))

	err = endpoint.Initialize("", responseChan)
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
		assert.Equal(t, "", resp.WatcherID)
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

func TestNewHTTPEndpoint(t *testing.T) {
	url, err := url.Parse("http://example.com")
	assert.NoError(t, err, "failed to parse URL")
	header := make(http.Header)
	payload := []byte(`{"key":"value"}`)

	endpoint := NewHTTPEndpoint(url, header, payload)
	require.NotNil(t, endpoint)

	assert.Equal(t, url, endpoint.URL)
	assert.Equal(t, header, endpoint.Header)
	assert.Equal(t, payload, endpoint.Payload)
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

	t.Run("PersistentJSONRPC", func(t *testing.T) {
		conn := &WSEndpoint{
			URL:    url,
			Header: header,
			Mode:   PersistentJSONRPC,
		}

		assert.Equal(t, url, conn.URL)
		assert.Equal(t, header, conn.Header)
		assert.Nil(t, conn.respChan)

		err = conn.Initialize("", responseChan)
		assert.NoError(t, err)
		assert.NotNil(t, conn.respChan)
		assert.NotNil(t, conn.conn)
		assert.NotNil(t, conn.ctx)
		assert.NotNil(t, conn.cancel)
	})

	t.Run("OneHitText", func(t *testing.T) {
		conn := &WSEndpoint{
			URL:    url,
			Header: header,
			Mode:   OneHitText,
		}

		assert.Equal(t, url, conn.URL)
		assert.Equal(t, header, conn.Header)
		assert.Nil(t, conn.respChan)

		err = conn.Initialize("", responseChan)
		assert.NoError(t, err)
		assert.NotNil(t, conn.respChan)
		assert.Nil(t, conn.conn) // no connection should be established since wsOneHit is used
		assert.NotNil(t, conn.ctx)
		assert.NotNil(t, conn.cancel)
	})
}

func TestWSEndpointExecutewsPersistent(t *testing.T) {
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
		Mode:    PersistentJSONRPC,
		Payload: []byte(payload),
	}

	err = endpoint.Initialize("", responseChan)
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
	var inflightMsg wsInflightMessage
	endpoint.inflightMsgs.Range(func(key, value interface{}) bool {
		inflightMsg = value.(wsInflightMessage)
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
		assert.Equal(t, "", resp.WatcherID)
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
	case <-time.After(1 * time.Second):
		t.Fatal("timeout waiting for response")
	}

	// Close the endpoint's internal connection and try to execute again
	err = endpoint.conn.Close()
	assert.NoError(t, err)
	endpoint.conn = nil
	// TODO: also test the case where the connection is closed by the server

	wg.Add(1)
	go func() {
		err := task.Execute()
		assert.NoError(t, err)
		wg.Done()
	}()

	wg.Wait()

	select {
	case resp := <-responseChan:
		assert.NotNil(t, resp)
		assert.NotNil(t, resp.Payload)
		assert.NoError(t, resp.Err)
		assert.Equal(t, url, resp.URL)

		assert.NotNil(t, endpoint.conn)

		metadata := resp.Metadata()
		assert.NotNil(t, metadata)
		assert.Equal(t, len(expectedResp), int(metadata.Size))
	case <-time.After(1 * time.Second):
		t.Fatal("timeout waiting for response")
	}
}

func TestWSEndpointExecutewsOneHit(t *testing.T) {
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
		Mode:    OneHitText,
		Payload: []byte(`{"key":"value"}`),
	}

	err = endpoint.Initialize("", responseChan)
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
		assert.Equal(t, "", resp.WatcherID)
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
		Mode:   PersistentJSONRPC,
	}

	err = conn.Initialize("", responseChan)
	assert.NoError(t, err)

	err = conn.connect()
	assert.Error(t, err)

	err = conn.reconnect()
	assert.NoError(t, err)
}

func TestNewWSEndpoint(t *testing.T) {
	url, err := url.Parse("ws://example.com/ws")
	assert.NoError(t, err, "failed to parse URL")
	header := make(http.Header)
	payload := []byte(`{"key":"value"}`)

	t.Run("ModeUnknown mode", func(t *testing.T) {
		endpoint := NewWSEndpoint(url, header, ModeUnknown, payload)
		require.NotNil(t, endpoint)

		assert.Equal(t, url, endpoint.URL)
		assert.Equal(t, header, endpoint.Header)
		assert.Equal(t, payload, endpoint.Payload)
		assert.Equal(t, ModeUnknown, endpoint.Mode)

		// Initialize should set the mode to default mode OneHitText
		endpoint.Initialize("", make(chan WatcherResponse))
		assert.Equal(t, OneHitText, endpoint.Mode)
	})

	t.Run("OneHitText mode", func(t *testing.T) {
		endpoint := NewWSEndpoint(url, header, OneHitText, payload)
		require.NotNil(t, endpoint)

		assert.Equal(t, url, endpoint.URL)
		assert.Equal(t, header, endpoint.Header)
		assert.Equal(t, payload, endpoint.Payload)
		assert.Equal(t, OneHitText, endpoint.Mode)
	})

	t.Run("PersistentJSONRPC mode", func(t *testing.T) {
		endpoint := NewWSEndpoint(url, header, PersistentJSONRPC, payload)
		require.NotNil(t, endpoint)

		assert.Equal(t, url, endpoint.URL)
		assert.Equal(t, header, endpoint.Header)
		assert.Equal(t, payload, endpoint.Payload)
		assert.Equal(t, PersistentJSONRPC, endpoint.Mode)
	})
}
