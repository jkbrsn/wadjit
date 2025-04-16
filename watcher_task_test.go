package wadjit

import (
	"net/http"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/jakobilobi/go-jsonrpc"
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

	endpoint := NewHTTPEndpoint(url, http.MethodGet, header, nil, "an-id")

	assert.Equal(t, url, endpoint.URL)
	assert.Equal(t, header, endpoint.Header)
	assert.Equal(t, "an-id", endpoint.ID)
	assert.Nil(t, endpoint.respChan)

	err := endpoint.Initialize("a-watcher-id", responseChan)
	assert.NoError(t, err)
	assert.NotNil(t, endpoint.respChan)
	assert.Equal(t, "a-watcher-id", endpoint.watcherID)
}

func TestHTTPEndpointExecute(t *testing.T) {
	server := echoServer()
	defer server.Close()

	url, err := url.Parse(server.URL)
	assert.NoError(t, err, "failed to parse HTTP URL")
	header := make(http.Header)
	responseChan := make(chan WatcherResponse, 1)

	header.Add("Content-Type", "application/json")

	endpoint := NewHTTPEndpoint(url, http.MethodPost, header, []byte(`{"key":"value"}`), "an-id")

	err = endpoint.Initialize("some-watcher-id", responseChan)
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
		assert.Equal(t, endpoint.ID, resp.TaskID)
		assert.Equal(t, endpoint.watcherID, resp.WatcherID)
		assert.Equal(t, url, resp.URL)
		assert.NoError(t, resp.Err)
		assert.NotNil(t, resp.Payload)
		// Check the response metadata
		metadata := resp.Metadata()
		assert.NotNil(t, metadata)
		assert.Equal(t, "application/json", metadata.Headers.Get("Content-Type"))
		assert.Greater(t, metadata.Latency, time.Duration(0))
		assert.Greater(t, metadata.TimeReceived, metadata.TimeSent)
		// Check the response data
		data, err := resp.Data()
		assert.NoError(t, err)
		assert.JSONEq(t, `{"key":"value"}`, string(data))
	case <-time.After(1 * time.Second):
		t.Fatal("timeout waiting for response")
	}
}

func TestHTTPEndpointExecuteMethods(t *testing.T) {
	server := echoServer()
	defer server.Close()

	echoURL, err := url.Parse(server.URL)
	assert.NoError(t, err, "failed to parse HTTP URL")
	header := make(http.Header)
	responseChan := make(chan WatcherResponse, 1)

	cases := []struct {
		method  string
		path    string
		payload []byte
	}{
		{http.MethodDelete, "/delete", nil},
		{http.MethodGet, "/get", nil},
		{http.MethodOptions, "/options", nil},
		{http.MethodPatch, "/patch", []byte(`{"key":"value"}`)},
		{http.MethodPost, "/post", []byte(`{"key":"value"}`)},
		{http.MethodPut, "/put", []byte(`{"key":"value"}`)},
	}

	for _, c := range cases {
		t.Run(c.method, func(t *testing.T) {
			if c.payload != nil {
				header.Add("Content-Type", "application/json")
			}
			echoURL.Path = c.path
			endpoint := NewHTTPEndpoint(echoURL, c.method, header, c.payload, "an-id")
			err = endpoint.Initialize("some-watcher-id", responseChan)
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
				assert.Equal(t, endpoint.ID, resp.TaskID)
				assert.Equal(t, endpoint.watcherID, resp.WatcherID)
				assert.Equal(t, echoURL, resp.URL)
				assert.NoError(t, resp.Err)

				// Check the response metadata
				metadata := resp.Metadata()
				assert.NotNil(t, metadata)
				assert.Greater(t, metadata.Latency, time.Duration(0))
				assert.Greater(t, metadata.TimeReceived, metadata.TimeSent)
				if c.payload != nil {
					assert.Equal(t, "application/json", metadata.Headers.Get("Content-Type"))
				}

				// Check the response data
				data, err := resp.Data()
				assert.NoError(t, err)
				if c.payload != nil {
					assert.JSONEq(t, `{"key":"value"}`, string(data))
				} else {
					assert.Contains(t, string(data), c.method)
				}
			case <-time.After(200 * time.Millisecond):
				t.Fatal("timeout waiting for response")
			}
		})
	}

}

func TestNewHTTPEndpoint(t *testing.T) {
	url, err := url.Parse("http://example.com")
	assert.NoError(t, err, "failed to parse URL")
	header := make(http.Header)
	payload := []byte(`{"key":"value"}`)

	endpoint := NewHTTPEndpoint(url, http.MethodPost, header, payload, "")
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

		err = conn.Initialize("some-watcher-id", responseChan)
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

		err = conn.Initialize("some-watcher-id", responseChan)
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
	payload := []byte(`{"id":"` + originalID + `","method":"echo","params":["test"],"jsonrpc":"2.0"}`)
	endpoint := &WSEndpoint{
		URL:     url,
		Header:  header,
		Mode:    PersistentJSONRPC,
		Payload: payload,
		ID:      "an-id",
	}

	resp := jsonrpc.Response{
		JSONRPC: "2.0",
		ID:      originalID,
		Result:  payload,
	}
	expectedResp, err := resp.MarshalJSON()
	require.NoError(t, err)

	err = endpoint.Initialize("some-watcher-id", responseChan)
	assert.NoError(t, err)

	task := endpoint.Task()
	assert.NotNil(t, task)

	t.Run("Inflight message", func(t *testing.T) {
		var wg sync.WaitGroup
		wg.Add(1)
		go func() {
			err := task.Execute()
			assert.NoError(t, err)
			wg.Done()
		}()
		wg.Wait()

		length := 0
		endpoint.inflightMsgs.Range(func(key, value any) bool {
			length++
			return true
		})
		assert.Equal(t, 1, length)
		var inflightMsg wsInflightMessage
		endpoint.inflightMsgs.Range(func(key, value any) bool {
			inflightMsg = value.(wsInflightMessage)
			return false // Stop after the first item
		})
		assert.Equal(t, originalID, inflightMsg.originalID)
		expectedResult := []byte(`{"id":"` + inflightMsg.inflightID + `","method":"echo","params":["test"],"jsonrpc":"2.0"}`)
		resp := jsonrpc.Response{
			JSONRPC: "2.0",
			ID:      originalID,
			Result:  expectedResult,
		}
		expectedResp, err := resp.MarshalJSON()
		require.NoError(t, err)

		select {
		case resp := <-responseChan:
			assert.NotNil(t, resp)
			assert.Equal(t, endpoint.ID, resp.TaskID)
			assert.Equal(t, endpoint.watcherID, resp.WatcherID)
			assert.Equal(t, url, resp.URL)
			require.NoError(t, resp.Err)
			assert.NotNil(t, resp.Payload)
			// Check the response metadata
			metadata := resp.Metadata()
			assert.NotNil(t, metadata)
			assert.Nil(t, metadata.Headers)
			assert.Zero(t, metadata.StatusCode)
			assert.Equal(t, len(expectedResp), int(metadata.Size))
			assert.Greater(t, metadata.Latency, time.Duration(0))
			assert.Greater(t, metadata.TimeReceived, metadata.TimeSent)
			// Check the response data
			data, err := resp.Data()
			assert.NoError(t, err)
			assert.JSONEq(t, string(expectedResp), string(data))
		case <-time.After(1 * time.Second):
			t.Fatal("timeout waiting for response")
		}
	})

	t.Run("Reconnect after client side close", func(t *testing.T) {
		err = endpoint.closeConn()
		assert.NoError(t, err)

		var wg sync.WaitGroup
		wg.Add(1)
		go func() {
			err := task.Execute()
			assert.NoError(t, err)
			wg.Done()
		}()
		wg.Wait()

		resp := jsonrpc.Response{
			JSONRPC: "2.0",
			ID:      originalID,
			Result:  payload,
		}
		expectedResp, err := resp.MarshalJSON()
		require.NoError(t, err)

		select {
		case resp := <-responseChan:
			assert.NotNil(t, resp)
			assert.Equal(t, endpoint.ID, resp.TaskID)
			assert.Equal(t, endpoint.watcherID, resp.WatcherID)
			assert.NotNil(t, resp.Payload)
			assert.NoError(t, resp.Err)
			assert.Equal(t, url, resp.URL)

			assert.NotNil(t, endpoint.conn)

			metadata := resp.Metadata()
			assert.NotNil(t, metadata)
			assert.Equal(t, len(expectedResp), int(metadata.Size)) // Payload size should match
			assert.Greater(t, metadata.Latency, time.Duration(0))
			assert.Greater(t, metadata.TimeReceived, metadata.TimeSent)
		case <-time.After(1 * time.Second):
			t.Fatal("timeout waiting for response")
		}
	})

	t.Run("Reconnect after server side close", func(t *testing.T) {
		server := jsonRPCServerWithServerDisconnect()
		defer server.Close()

		wsURL := "ws" + server.URL[4:] + "/ws"
		url, err := url.Parse(wsURL)
		assert.NoError(t, err, "failed to parse URL")
		responseChan := make(chan WatcherResponse, 2)

		endpoint := &WSEndpoint{
			URL:     url,
			Header:  header,
			Mode:    PersistentJSONRPC,
			Payload: payload,
			ID:      "another-id",
		}

		err = endpoint.Initialize("some-watcher-id", responseChan)
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

		select {
		case resp := <-responseChan:
			assert.NotNil(t, resp)
			assert.Equal(t, endpoint.ID, resp.TaskID)
			assert.Equal(t, endpoint.watcherID, resp.WatcherID)
			assert.NotNil(t, resp.Payload)
			assert.NoError(t, resp.Err)
			assert.Equal(t, url, resp.URL)

			//assert.NotNil(t, endpoint.conn)

			metadata := resp.Metadata()
			assert.NotNil(t, metadata)
			assert.Equal(t, len(expectedResp), int(metadata.Size)) // Payload size should match
			assert.Greater(t, metadata.Latency, time.Duration(0))
			assert.Greater(t, metadata.TimeReceived, metadata.TimeSent)
		case <-time.After(1 * time.Second):
			t.Fatal("timeout waiting for response")
		}

		time.Sleep(5 * time.Millisecond)
		// Connection should now have closed, try again

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
			assert.Equal(t, endpoint.ID, resp.TaskID)
			assert.Equal(t, endpoint.watcherID, resp.WatcherID)
			assert.NotNil(t, resp.Payload)
			assert.NoError(t, resp.Err)
			assert.Equal(t, url, resp.URL)

			//assert.NotNil(t, endpoint.conn)

			metadata := resp.Metadata()
			assert.NotNil(t, metadata)
			assert.Equal(t, len(expectedResp), int(metadata.Size)) // Payload size should match
			assert.Greater(t, metadata.Latency, time.Duration(0))
			assert.Greater(t, metadata.TimeReceived, metadata.TimeSent)
		case <-time.After(1 * time.Second):
			t.Fatal("timeout waiting for response")
		}
	})
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
		ID:      "an-id",
	}

	err = endpoint.Initialize("some-watcher-id", responseChan)
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
		assert.Equal(t, endpoint.ID, resp.TaskID)
		assert.Equal(t, endpoint.watcherID, resp.WatcherID)
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
		assert.Greater(t, metadata.TimeReceived, metadata.TimeSent)
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

	err = conn.Initialize("some-watcher-id", responseChan)
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
		endpoint := NewWSEndpoint(url, header, ModeUnknown, payload, "")
		require.NotNil(t, endpoint)

		assert.Equal(t, url, endpoint.URL)
		assert.Equal(t, header, endpoint.Header)
		assert.Equal(t, payload, endpoint.Payload)
		assert.Equal(t, ModeUnknown, endpoint.Mode)

		// Initialize should set the mode to default mode OneHitText
		endpoint.Initialize("some-watcher-id", make(chan WatcherResponse))
		assert.Equal(t, OneHitText, endpoint.Mode)
	})

	t.Run("OneHitText mode", func(t *testing.T) {
		endpoint := NewWSEndpoint(url, header, OneHitText, payload, "")
		require.NotNil(t, endpoint)

		assert.Equal(t, url, endpoint.URL)
		assert.Equal(t, header, endpoint.Header)
		assert.Equal(t, payload, endpoint.Payload)
		assert.Equal(t, OneHitText, endpoint.Mode)
	})

	t.Run("PersistentJSONRPC mode", func(t *testing.T) {
		endpoint := NewWSEndpoint(url, header, PersistentJSONRPC, payload, "")
		require.NotNil(t, endpoint)

		assert.Equal(t, url, endpoint.URL)
		assert.Equal(t, header, endpoint.Header)
		assert.Equal(t, payload, endpoint.Payload)
		assert.Equal(t, PersistentJSONRPC, endpoint.Mode)
	})
}
