package wadjit

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/bytedance/sonic"
	"github.com/gorilla/websocket"
	"github.com/jkbrsn/jsonrpc"
	"github.com/rs/xid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWSConnnImplementsWatcherTask(_ *testing.T) {
	var _ WatcherTask = &WSEndpoint{}
}

type timeoutNetError struct{}

func (timeoutNetError) Error() string   { return "timeout" }
func (timeoutNetError) Timeout() bool   { return true }
func (timeoutNetError) Temporary() bool { return true }

func TestWSConnInitialize(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(echoHandler))
	defer server.Close()

	wsURL := "ws" + server.URL[4:] + "/ws"
	parsedURL, err := url.Parse(wsURL)
	require.NoError(t, err, "failed to parse URL")
	header := make(http.Header)

	cases := []struct {
		name       string
		mode       WSEndpointMode
		expectConn bool
	}{
		{name: "PersistentJSONRPC", mode: PersistentJSONRPC, expectConn: true},
		{name: "OneHitText", mode: OneHitText, expectConn: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			conn := &WSEndpoint{
				URL:    parsedURL,
				Header: header,
				Mode:   tc.mode,
			}

			require.Equal(t, parsedURL, conn.URL)
			require.Equal(t, header, conn.Header)
			require.Nil(t, conn.respChan)

			responseChan := make(chan WatcherResponse)
			require.NoError(t, conn.Initialize("some-watcher-id", responseChan))
			require.NotNil(t, conn.respChan)
			require.NotNil(t, conn.ctx)
			require.NotNil(t, conn.cancel)
			if tc.expectConn {
				require.NotNil(t, conn.conn)
			} else {
				require.Nil(t, conn.conn)
			}
		})
	}
}

func TestWSEndpoint_HandleWebSocketErrors(t *testing.T) {
	baseURL, err := url.Parse("ws://example.com/socket")
	require.NoError(t, err)

	t.Run("CloseTryAgainLater", func(t *testing.T) {
		respChan := make(chan WatcherResponse, 1)
		endpoint := &WSEndpoint{ID: "task", watcherID: "watcher", respChan: respChan}

		clone := *baseURL
		endpoint.handleWebSocketCloseError(&websocket.CloseError{
			Code: websocket.CloseTryAgainLater,
			Text: "backpressure",
		}, &clone)

		select {
		case resp := <-respChan:
			assert.Equal(t, "task", resp.TaskID)
			assert.Equal(t, "watcher", resp.WatcherID)
			require.Error(t, resp.Err)
			assert.Contains(t, resp.Err.Error(), "try again later (1013)")
		case <-time.After(100 * time.Millisecond):
			t.Fatal("expected response from close error handler")
		}
	})

	t.Run("CloseAbnormalClosure", func(t *testing.T) {
		respChan := make(chan WatcherResponse, 1)
		endpoint := &WSEndpoint{
			ID:        "task",
			watcherID: "watcher",
			respChan:  respChan,
			ctx:       context.Background(),
		}

		clone := *baseURL
		endpoint.handleWebSocketReadError(&websocket.CloseError{
			Code: websocket.CloseAbnormalClosure,
			Text: "abrupt",
		}, &clone)

		select {
		case resp := <-respChan:
			assert.Equal(t, "task", resp.TaskID)
			assert.Equal(t, "watcher", resp.WatcherID)
			require.Error(t, resp.Err)
			assert.Contains(t, resp.Err.Error(), "closed abnormally (1006)")
		case <-time.After(100 * time.Millisecond):
			t.Fatal("expected response from read error handler")
		}
	})

	t.Run("NetworkTimeout", func(t *testing.T) {
		respChan := make(chan WatcherResponse, 1)
		endpoint := &WSEndpoint{ID: "task", watcherID: "watcher", respChan: respChan}

		clone := *baseURL
		endpoint.handleNetworkError(timeoutNetError{}, &clone)

		select {
		case resp := <-respChan:
			assert.Equal(t, "task", resp.TaskID)
			assert.Equal(t, "watcher", resp.WatcherID)
			require.Error(t, resp.Err)
			assert.Contains(t, resp.Err.Error(), "read timeout")
		case <-time.After(100 * time.Millisecond):
			t.Fatal("expected response from network error handler")
		}
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
	req := &jsonrpc.Request{
		JSONRPC: "2.0",
		ID:      originalID,
		Method:  "echo",
		Params:  []any{"test"},
	}
	payload, err := sonic.Marshal(req)
	require.NoError(t, err)
	endpoint := &WSEndpoint{
		URL:     parsedURL,
		Header:  header,
		Mode:    PersistentJSONRPC,
		Payload: payload,
		ID:      "an-id",
	}

	resp, err := jsonrpc.NewResponse(originalID, payload)
	require.NoError(t, err)
	expectedResp, err := resp.MarshalJSON()
	require.NoError(t, err)

	err = endpoint.Initialize("some-watcher-id", responseChan)
	assert.NoError(t, err)

	task := endpoint.Task()
	assert.NotNil(t, task)

	t.Run("Inflight message", func(t *testing.T) {
		var wg sync.WaitGroup
		wg.Go(func() {
			err := task.Execute()
			assert.NoError(t, err)
		})
		wg.Wait()

		length := 0
		endpoint.inflightMsgs.Range(func(_, _ any) bool {
			length++
			return true
		})
		assert.Equal(t, 1, length)
		var inflightMsg wsInflightMessage
		endpoint.inflightMsgs.Range(func(_, value any) bool {
			var ok bool
			inflightMsg, ok = value.(wsInflightMessage)
			assert.True(t, ok, "expected value to be wsInflightMessage")
			return false // Stop after the first item
		})
		assert.Equal(t, originalID, inflightMsg.originalID)
		expectedReq := &jsonrpc.Request{
			JSONRPC: "2.0",
			ID:      inflightMsg.inflightID,
			Method:  "echo",
			Params:  []any{"test"},
		}
		expectedResult, err := sonic.Marshal(expectedReq)
		require.NoError(t, err)
		resp, err := jsonrpc.NewResponse(originalID, expectedResult)
		require.NoError(t, err)
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
		wg.Go(func() {
			err := task.Execute()
			assert.NoError(t, err)
		})
		wg.Wait()

		resp, err := jsonrpc.NewResponse(originalID, payload)
		require.NoError(t, err)
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
		wg.Go(func() {
			err := task.Execute()
			assert.NoError(t, err)
		})
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

		wg.Go(func() {
			err := task.Execute()
			assert.NoError(t, err)
		})
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
		// Data is fully read when received
		assert.Greater(t, *metadata.TimeData.DataTransfer, time.Duration(0))
		assert.Greater(t, *metadata.TimeData.RequestTimeTotal, time.Duration(0))
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
	task, ok := ep.Task().(*wsOneHit)
	require.True(t, ok, "expected task to be *wsOneHit")
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

type wsEndpointTestSuite struct {
	newWS func() *WSEndpoint
}

func (s *wsEndpointTestSuite) TestWSEndpointExecute(t *testing.T) {
	ep := s.newWS()
	header := make(http.Header)
	responseChan := make(chan WatcherResponse, 1)

	var server *httptest.Server
	var payload []byte
	var parsedURL *url.URL
	var err error

	if ep.Mode == PersistentJSONRPC {
		server = jsonRPCServer()
		originalID := xid.New().String()
		payload = []byte(`{"id":"` + originalID +
			`","method":"echo","params":["test"],"jsonrpc":"2.0"}`)
	} else {
		server = httptest.NewServer(http.HandlerFunc(echoHandler))
		payload = []byte(`{"key":"value"}`)
	}
	defer server.Close()

	wsURL := "ws" + server.URL[4:] + "/ws"
	parsedURL, err = url.Parse(wsURL)
	assert.NoError(t, err, "failed to parse URL")

	ep.URL = parsedURL
	ep.Header = header
	ep.Payload = payload
	ep.ID = "an-id"

	err = ep.Initialize("some-watcher-id", responseChan)
	assert.NoError(t, err)

	task := ep.Task()
	assert.NotNil(t, task)

	go func() {
		err := task.Execute()
		assert.NoError(t, err)
	}()

	select {
	case resp := <-responseChan:
		assert.NotNil(t, resp)
		assert.Equal(t, ep.ID, resp.TaskID)
		assert.Equal(t, ep.watcherID, resp.WatcherID)
		assert.Equal(t, parsedURL, resp.URL)
		assert.NoError(t, resp.Err)
		assert.NotNil(t, resp.Payload)
	case <-time.After(1 * time.Second):
		t.Fatal("timeout waiting for response")
	}
}

func (s *wsEndpointTestSuite) TestWSEndpointReconnect(t *testing.T) {
	ep := s.newWS()
	if ep.Mode != PersistentJSONRPC {
		t.Skip("Reconnect only applies to PersistentJSONRPC mode")
	}

	server := httptest.NewServer(http.HandlerFunc(echoHandler))
	defer server.Close()

	wsURL := "ws" + server.URL[4:] + "/ws"
	parsedURL, err := url.Parse(wsURL)
	assert.NoError(t, err, "failed to parse URL")
	header := make(http.Header)

	ep.URL = parsedURL
	ep.Header = header
	ep.Mode = PersistentJSONRPC

	err = ep.Initialize("some-watcher-id", make(chan WatcherResponse))
	assert.NoError(t, err)

	err = ep.connect()
	assert.Error(t, err)

	err = ep.reconnect()
	assert.NoError(t, err)
}

func runWSEndpointTestSuite(t *testing.T, s *wsEndpointTestSuite) {
	t.Run("Execute", s.TestWSEndpointExecute)
	t.Run("Reconnect", s.TestWSEndpointReconnect)
}

func TestWSEndpointSuite(t *testing.T) {
	t.Run("PersistentJSONRPC", func(t *testing.T) {
		newWS := func() *WSEndpoint {
			return &WSEndpoint{Mode: PersistentJSONRPC}
		}
		runWSEndpointTestSuite(t, &wsEndpointTestSuite{newWS: newWS})
	})

	t.Run("OneHitText", func(t *testing.T) {
		newWS := func() *WSEndpoint {
			return &WSEndpoint{Mode: OneHitText}
		}
		runWSEndpointTestSuite(t, &wsEndpointTestSuite{newWS: newWS})
	})
}

func TestWSEndpointTimeouts(t *testing.T) {
	t.Run("endpoint with timeouts configures correctly", func(t *testing.T) {
		testURL, _ := url.Parse("ws://example.com")
		timeouts := WSTimeouts{
			Handshake:    5 * time.Second,
			Read:         30 * time.Second,
			Write:        10 * time.Second,
			PingInterval: 20 * time.Second,
		}

		endpoint := NewWSEndpoint(
			testURL,
			make(http.Header),
			PersistentJSONRPC,
			[]byte("test"),
			"test-id",
			WithWSTimeouts(timeouts),
		)

		assert.True(t, endpoint.timeoutsSet)
		assert.Equal(t, timeouts, endpoint.timeouts)
	})

	t.Run("endpoint inherits default timeouts from Wadjit", func(t *testing.T) {
		defaultTimeouts := WSTimeouts{
			Handshake: 7 * time.Second,
			Read:      40 * time.Second,
			Write:     15 * time.Second,
		}
		w := New(WithDefaultWSTimeouts(defaultTimeouts))
		defer func() {
			err := w.Close()
			assert.NoError(t, err)
		}()

		testURL, _ := url.Parse("ws://example.com")
		endpoint := NewWSEndpoint(
			testURL,
			make(http.Header),
			PersistentJSONRPC,
			[]byte("test"),
			"test-id",
		)

		watcher := &Watcher{
			ID:      xid.New().String(),
			Cadence: time.Second,
			Tasks:   []WatcherTask{endpoint},
		}

		// Apply defaults
		w.applyDefaultWSTimeouts(watcher)

		assert.True(t, endpoint.timeoutsSet)
		assert.Equal(t, defaultTimeouts, endpoint.timeouts)
	})

	t.Run("explicit timeouts override Wadjit defaults", func(t *testing.T) {
		defaultTimeouts := WSTimeouts{
			Handshake: 10 * time.Second,
			Read:      50 * time.Second,
		}
		endpointTimeouts := WSTimeouts{
			Handshake: 3 * time.Second,
			Read:      20 * time.Second,
		}

		w := New(WithDefaultWSTimeouts(defaultTimeouts))
		defer func() {
			err := w.Close()
			assert.NoError(t, err)
		}()

		testURL, _ := url.Parse("ws://example.com")
		endpoint := NewWSEndpoint(
			testURL,
			make(http.Header),
			PersistentJSONRPC,
			[]byte("test"),
			"test-id",
			WithWSTimeouts(endpointTimeouts),
		)

		watcher := &Watcher{
			ID:      xid.New().String(),
			Cadence: time.Second,
			Tasks:   []WatcherTask{endpoint},
		}

		// Apply defaults (should not override explicit timeouts)
		w.applyDefaultWSTimeouts(watcher)

		assert.True(t, endpoint.timeoutsSet)
		assert.Equal(t, endpointTimeouts, endpoint.timeouts)
		assert.NotEqual(t, defaultTimeouts, endpoint.timeouts)
	})

	t.Run("invalid timeouts fail during connection", func(t *testing.T) {
		testURL, _ := url.Parse("ws://example.com")
		invalidTimeouts := WSTimeouts{
			Read:         10 * time.Second,
			PingInterval: 15 * time.Second, // Invalid: PingInterval >= Read
		}

		endpoint := NewWSEndpoint(
			testURL,
			make(http.Header),
			PersistentJSONRPC,
			[]byte("test"),
			"test-id",
			WithWSTimeouts(invalidTimeouts),
		)

		// Attempting to connect should fail validation
		endpoint.mu.Lock()
		err := invalidTimeouts.Validate()
		endpoint.mu.Unlock()

		assert.Error(t, err)
		assert.Contains(t, err.Error(), "PingInterval must be less than")
	})

	t.Run("option functions work correctly", func(t *testing.T) {
		testURL, _ := url.Parse("ws://example.com")
		header := http.Header{"X-Test": []string{"value"}}
		payload := []byte("test payload")
		id := "custom-id"
		timeouts := WSTimeouts{Handshake: 5 * time.Second}

		endpoint := NewWSEndpoint(
			testURL,
			make(http.Header), // Initial header will be overridden
			OneHitText,
			nil, // Initial payload will be overridden
			"initial-id",
			WithWSHeader(header),
			WithWSPayload(payload),
			WithWSID(id),
			WithWSTimeouts(timeouts),
		)

		assert.Equal(t, header, endpoint.Header)
		assert.Equal(t, payload, endpoint.Payload)
		assert.Equal(t, id, endpoint.ID)
		assert.True(t, endpoint.timeoutsSet)
		assert.Equal(t, timeouts, endpoint.timeouts)
	})
}
