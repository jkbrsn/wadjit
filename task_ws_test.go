package wadjit

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/jkbrsn/go-jsonrpc"
	"github.com/rs/xid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWSConnnImplementsWatcherTask(_ *testing.T) {
	var _ WatcherTask = &WSEndpoint{}
}

func TestWSConnInitialize(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(echoHandler))
	defer server.Close()

	wsURL := "ws" + server.URL[4:] + "/ws"
	parsedURL, err := url.Parse(wsURL)
	assert.NoError(t, err, "failed to parse URL")
	header := make(http.Header)
	responseChan := make(chan WatcherResponse)

	t.Run("PersistentJSONRPC", func(t *testing.T) {
		conn := &WSEndpoint{
			URL:    parsedURL,
			Header: header,
			Mode:   PersistentJSONRPC,
		}

		assert.Equal(t, parsedURL, conn.URL)
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
			URL:    parsedURL,
			Header: header,
			Mode:   OneHitText,
		}

		assert.Equal(t, parsedURL, conn.URL)
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
	parsedURL, err := url.Parse(wsURL)
	assert.NoError(t, err, "failed to parse URL")
	header := make(http.Header)
	responseChan := make(chan WatcherResponse, 2)

	originalID := xid.New().String()
	payload := []byte(`{"id":"` + originalID + `","method":"echo","params":["test"],"jsonrpc":"2.0"}`)
	endpoint := &WSEndpoint{
		URL:     parsedURL,
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
		endpoint.inflightMsgs.Range(func(_, _ any) bool {
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
			assert.Equal(t, parsedURL, resp.URL)
			require.NoError(t, resp.Err)
			assert.NotNil(t, resp.Payload)
			// Check the response metadata
			metadata := resp.Metadata()
			assert.NotNil(t, metadata)
			assert.Nil(t, metadata.Headers)
			assert.Zero(t, metadata.StatusCode)
			assert.Equal(t, len(expectedResp), int(metadata.Size))
			assert.Greater(t, metadata.TimeData.Latency, time.Duration(0))
			assert.Greater(t, metadata.TimeData.ReceivedAt, metadata.TimeData.SentAt)
			// Check the response data
			data, err := resp.Data()
			assert.NoError(t, err)
			assert.JSONEq(t, string(expectedResp), string(data))
			// Mutating the response URL must not affect the endpoint’s URL.
			origPath := endpoint.URL.Path // remember original
			resp.URL.Path = "/tampered"   // mutate the copy
			assert.NotEqual(t, resp.URL, endpoint.URL, "pointers should differ")
			assert.Equal(t, origPath, endpoint.URL.Path, "endpoint URL must stay unchanged")
			assert.Equal(t, "/tampered", resp.URL.Path)
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
			assert.Equal(t, parsedURL, resp.URL)

			assert.NotNil(t, endpoint.conn)

			metadata := resp.Metadata()
			assert.NotNil(t, metadata)
			assert.Equal(t, len(expectedResp), int(metadata.Size)) // Payload size should match
			assert.Greater(t, metadata.TimeData.Latency, time.Duration(0))
			assert.Greater(t, metadata.TimeData.ReceivedAt, metadata.TimeData.SentAt)
		case <-time.After(1 * time.Second):
			t.Fatal("timeout waiting for response")
		}
	})

	t.Run("Reconnect after server side close", func(t *testing.T) {
		server := jsonRPCServerWithServerDisconnect()
		defer server.Close()

		wsURL := "ws" + server.URL[4:] + "/ws"
		parsedURL, err := url.Parse(wsURL)
		assert.NoError(t, err, "failed to parse URL")
		responseChan := make(chan WatcherResponse, 2)

		endpoint := &WSEndpoint{
			URL:     parsedURL,
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
			assert.Equal(t, parsedURL, resp.URL)

			// assert.NotNil(t, endpoint.conn)

			metadata := resp.Metadata()
			assert.NotNil(t, metadata)
			assert.Equal(t, len(expectedResp), int(metadata.Size)) // Payload size should match
			assert.Greater(t, metadata.TimeData.Latency, time.Duration(0))
			assert.Greater(t, metadata.TimeData.ReceivedAt, metadata.TimeData.SentAt)
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
			assert.Equal(t, parsedURL, resp.URL)

			// assert.NotNil(t, endpoint.conn)

			metadata := resp.Metadata()
			assert.NotNil(t, metadata)
			assert.Equal(t, len(expectedResp), int(metadata.Size)) // Payload size should match
			assert.Greater(t, metadata.TimeData.Latency, time.Duration(0))
			assert.Greater(t, metadata.TimeData.ReceivedAt, metadata.TimeData.SentAt)
		case <-time.After(1 * time.Second):
			t.Fatal("timeout waiting for response")
		}
	})
}

func TestWSEndpointExecutewsOneHit(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(echoHandler))
	defer server.Close()

	wsURL := "ws" + server.URL[4:] + "/ws"
	parsedURL, err := url.Parse(wsURL)
	assert.NoError(t, err, "failed to parse URL")
	header := make(http.Header)
	responseChan := make(chan WatcherResponse)

	endpoint := &WSEndpoint{
		URL:     parsedURL,
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
		assert.Equal(t, parsedURL, resp.URL)
		assert.NoError(t, resp.Err)
		assert.NotNil(t, resp.Payload)
		// Check the response metadata
		metadata := resp.Metadata()
		assert.NotNil(t, metadata)
		assert.Nil(t, metadata.Headers)
		assert.Zero(t, metadata.StatusCode)
		assert.Equal(t, len(endpoint.Payload), int(metadata.Size))
		assert.Greater(t, metadata.TimeData.Latency, time.Duration(0))
		assert.Greater(t, metadata.TimeData.ReceivedAt, metadata.TimeData.SentAt)
		assert.Greater(t, *metadata.TimeData.DataTransfer, time.Duration(0))     // Data is fully read when received
		assert.Greater(t, *metadata.TimeData.RequestTimeTotal, time.Duration(0)) // Data is fully read when received
		// Check the response data
		data, err := resp.Data()
		assert.NoError(t, err)
		assert.JSONEq(t, `{"key":"value"}`, string(data))
		// Mutating the response URL must not affect the endpoint’s URL.
		origPath := endpoint.URL.Path // remember original
		resp.URL.Path = "/tampered"   // mutate the copy
		assert.NotEqual(t, resp.URL, endpoint.URL, "pointers should differ")
		assert.Equal(t, origPath, endpoint.URL.Path, "endpoint URL must stay unchanged")
		assert.Equal(t, "/tampered", resp.URL.Path)
	case <-time.After(1 * time.Second):
		t.Fatal("timeout waiting for response")
	}
}

func TestWSConnReconnect(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(echoHandler))
	defer server.Close()

	wsURL := "ws" + server.URL[4:] + "/ws"
	parsedURL, err := url.Parse(wsURL)
	assert.NoError(t, err, "failed to parse URL")
	header := make(http.Header)
	responseChan := make(chan WatcherResponse)

	conn := &WSEndpoint{
		URL:    parsedURL,
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

func TestWSEndpoint_ResponseRemoteAddr(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(echoHandler))
	defer server.Close()

	// Build endpoint
	wsURL := "ws" + server.URL[4:] + "/ws"
	u, _ := url.Parse(wsURL)
	ep := NewWSEndpoint(u, http.Header{}, OneHitText, nil, "ws-id1")

	respChan := make(chan WatcherResponse, 1)
	require.NoError(t, ep.Initialize("watcher-1", respChan))

	// Run a one-shot WS task
	task := ep.Task().(*wsOneHit)
	require.NoError(t, task.Execute())

	// Check the response metadata for RemoteAddr
	resp := <-respChan
	md := resp.Metadata()
	require.Equal(t, server.Listener.Addr(), md.RemoteAddr)
	require.Greater(t, md.TimeData.Latency, time.Duration(0))
}

func TestNewWSEndpoint(t *testing.T) {
	parsedURL, err := url.Parse("ws://example.com/ws")
	assert.NoError(t, err, "failed to parse URL")
	header := make(http.Header)
	payload := []byte(`{"key":"value"}`)

	t.Run("ModeUnknown mode", func(t *testing.T) {
		endpoint := NewWSEndpoint(parsedURL, header, ModeUnknown, payload, "")
		require.NotNil(t, endpoint)

		assert.Equal(t, parsedURL, endpoint.URL)
		assert.Equal(t, header, endpoint.Header)
		assert.Equal(t, payload, endpoint.Payload)
		assert.Equal(t, ModeUnknown, endpoint.Mode)

		// Initialize should set the mode to default mode OneHitText
		require.NoError(t, endpoint.Initialize("some-watcher-id", make(chan WatcherResponse)))
		assert.Equal(t, OneHitText, endpoint.Mode)
	})

	t.Run("OneHitText mode", func(t *testing.T) {
		endpoint := NewWSEndpoint(parsedURL, header, OneHitText, payload, "")
		require.NotNil(t, endpoint)

		assert.Equal(t, parsedURL, endpoint.URL)
		assert.Equal(t, header, endpoint.Header)
		assert.Equal(t, payload, endpoint.Payload)
		assert.Equal(t, OneHitText, endpoint.Mode)
	})

	t.Run("PersistentJSONRPC mode", func(t *testing.T) {
		endpoint := NewWSEndpoint(parsedURL, header, PersistentJSONRPC, payload, "")
		require.NotNil(t, endpoint)

		assert.Equal(t, parsedURL, endpoint.URL)
		assert.Equal(t, header, endpoint.Header)
		assert.Equal(t, payload, endpoint.Payload)
		assert.Equal(t, PersistentJSONRPC, endpoint.Mode)
	})
}
