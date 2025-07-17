package wadjit

import (
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"testing"
	"time"

	"github.com/rs/xid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHTTPEndpointImplementsWatcherTask(t *testing.T) {
	var _ WatcherTask = &HTTPEndpoint{}
}

func TestHTTPEndpointInitialize(t *testing.T) {
	url, _ := url.Parse("http://example.com")
	header := make(http.Header)
	responseChan := make(chan WatcherResponse)

	endpoint := NewHTTPEndpoint(
		url,
		http.MethodGet,
		WithHeader(header),
		WithID("an-id"),
	)

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
	server := httptest.NewServer(http.HandlerFunc(echoHandler))
	defer server.Close()

	url, err := url.Parse(server.URL)
	assert.NoError(t, err, "failed to parse HTTP URL")
	header := make(http.Header)
	responseChan := make(chan WatcherResponse, 1)

	header.Add("Content-Type", "application/json")

	endpoint := NewHTTPEndpoint(
		url,
		http.MethodPost,
		WithHeader(header),
		WithPayload([]byte(`{"key":"value"}`)),
		WithID("an-id"),
	)

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
		// Timings
		assert.Greater(t, metadata.TimeData.Latency, time.Duration(0))
		assert.Greater(t, metadata.TimeData.ReceivedAt, metadata.TimeData.SentAt)
		assert.Nil(t, metadata.TimeData.DNSLookup) // No DNS lookup in this test
		assert.Greater(t, *metadata.TimeData.TCPConnect, time.Duration(0))
		assert.Zero(t, metadata.TimeData.TLSHandshake) // No TLS in this test
		assert.Greater(t, *metadata.TimeData.ServerProcessing, time.Duration(0))
		assert.Nil(t, metadata.TimeData.DataTransfer)      // Data not yet read
		assert.Zero(t, metadata.TimeData.RequestTimeTotal) // Data not yet read
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
		// Reload metadata to get updated timings
		metadata = resp.Metadata()
		assert.Greater(t, *metadata.TimeData.DataTransfer, time.Duration(0))
		assert.Greater(t, *metadata.TimeData.RequestTimeTotal, time.Duration(0))
	case <-time.After(1 * time.Second):
		t.Fatal("timeout waiting for response")
	}
}

func TestHTTPEndpointExecuteMethods(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(echoHandler))
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
			endpoint := NewHTTPEndpoint(
				echoURL,
				c.method,
				WithHeader(header),
				WithPayload(c.payload),
				WithID("an-id"),
			)
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
				assert.Greater(t, metadata.TimeData.Latency, time.Duration(0))
				assert.Greater(t, metadata.TimeData.ReceivedAt, metadata.TimeData.SentAt)
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

func TestHTTPEndpoint_ResponseRemoteAddr(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(echoHandler))
	defer server.Close()

	// Build a minimal endpoint that hits the test server
	u, _ := url.Parse(server.URL)
	ep := NewHTTPEndpoint(u, http.MethodGet, WithHeader(http.Header{}), WithID("id1"))
	respChan := make(chan WatcherResponse, 1)
	ep.Initialize("watcher-1", respChan)

	// Run one request synchronously
	task := ep.Task().(*httpRequest)
	require.NoError(t, task.Execute())

	// Check the response metadata for RemoteAddr
	resp := <-respChan
	metadata := resp.Metadata()
	assert.NotNil(t, metadata)
	assert.Greater(t, metadata.TimeData.Latency, time.Duration(0))
	require.Equal(t, server.Listener.Addr(), metadata.RemoteAddr)
}

func TestTransportControlBypassesDNS(t *testing.T) {
	// 1. Spin up a test server.
	server := httptest.NewServer(http.HandlerFunc(echoHandler))
	defer server.Close()

	// 2. Parse the URL but change the Host to something that cannot resolve.
	u, err := url.Parse(server.URL)
	require.NoError(t, err)
	fakeHost := "nonexistent.example.com"
	u.Host = fakeHost // keep scheme & port placeholders

	// 3. Extract the real IP:port from the listener for TransportControl.
	realAddr := server.Listener.Addr().(*net.TCPAddr)

	tc := &TransportControl{
		AddrPort:      realAddr.AddrPort(), // ip:randomPort
		TLSEnabled:    false,               // httptest.NewServer(http.HandlerFunc(echoHandler)) is http
		SkipTLSVerify: true,                // accept self-signed cert from httptest
	}

	// 4. Build endpoint with TransportControl.
	ep := NewHTTPEndpoint(u, http.MethodGet, WithID("id"), WithTransportControl(tc))
	responseChan := make(chan WatcherResponse, 1)
	require.NoError(t, ep.Initialize("wid", responseChan))

	// 5. Execute.
	go ep.Task().Execute()

	// 6. Assert.
	select {
	case resp := <-responseChan:
		assert.NoError(t, resp.Err)
		md := resp.Metadata()
		// DNSLookup should be nil because DialContext skipped it.
		assert.Nil(t, md.TimeData.DNSLookup)
		// Remote addr should match the server’s IP.
		serverAddr := net.TCPAddr{
			IP:   net.ParseIP(realAddr.IP.String()),
			Port: realAddr.Port,
		}
		assert.Equal(t, serverAddr.String(), resp.Metadata().RemoteAddr.String())
		assert.Equal(t, realAddr.AddrPort(), resp.Metadata().RemoteAddr.(*net.TCPAddr).AddrPort())
		assert.Equal(t, fakeHost, resp.URL.Hostname())
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for response")
	}
}

func TestTransportControlTLS(t *testing.T) {
	// HTTPS test server
	server := httptest.NewTLSServer(http.HandlerFunc(echoHandler))
	defer server.Close()

	// Fake hostname that will not resolve
	u, _ := url.Parse(server.URL)
	u.Host = "geo.example.com" // keeps :443 from server.URL

	real := server.Listener.Addr().(*net.TCPAddr)
	tc := &TransportControl{
		AddrPort:      real.AddrPort(), // ip:randomPort
		TLSEnabled:    true,
		SkipTLSVerify: true, // accept self-signed cert from httptest
	}

	ep := NewHTTPEndpoint(u, http.MethodGet, WithID("tls-test"), WithTransportControl(tc))
	respCh := make(chan WatcherResponse, 1)
	require.NoError(t, ep.Initialize("wid", respCh))

	go ep.Task().Execute()

	select {
	case resp := <-respCh:
		assert.NoError(t, resp.Err)
		md := resp.Metadata()
		assert.Nil(t, md.TimeData.DNSLookup)         // bypassed
		assert.NotZero(t, *md.TimeData.TLSHandshake) // handshake happened
		assert.Equal(t, real.AddrPort(),             // connected to real IP
			resp.Metadata().RemoteAddr.(*net.TCPAddr).AddrPort())
	case <-time.After(time.Second):
		t.Fatal("timeout")
	}
}

func TestConnectionReuse(t *testing.T) {
	// Start a simple HTTP test server that echoes.
	server := httptest.NewServer(http.HandlerFunc(echoHandler))
	defer server.Close()

	// Parse the server URL.
	u, err := url.Parse(server.URL)
	require.NoError(t, err)

	// Build endpoint that will reuse connections.
	ep := NewHTTPEndpoint(
		u,
		http.MethodGet,
		WithID("reuse"),
		WithReadFast(), // ensure body is read/closed so the conn can be reused
	)

	respCh := make(chan WatcherResponse, 1)
	require.NoError(t, ep.Initialize("wid", respCh))

	// 1st request – should establish a new TCP connection.
	require.NoError(t, ep.Task().Execute())

	resp1 := <-respCh
	require.NoError(t, resp1.Err)
	md1 := resp1.Metadata()
	assert.NotNil(t, md1.TimeData.TCPConnect, "first request should incur TCP connect")

	// 2nd request – should reuse the existing connection (no new ConnectStart/Done callbacks).
	require.NoError(t, ep.Task().Execute())

	resp2 := <-respCh
	require.NoError(t, resp2.Err)
	md2 := resp2.Metadata()
	assert.Nil(t, md2.TimeData.TCPConnect, "second request should reuse connection (no TCP connect)")

	// Remote address must be the same for both requests.
	assert.Equal(t, md1.RemoteAddr.String(), md2.RemoteAddr.String())
}

func TestNewHTTPEndpoint(t *testing.T) {
	url, err := url.Parse("http://example.com")
	assert.NoError(t, err, "failed to parse URL")

	cases := []struct {
		name    string
		options []HTTPEndpointOption
	}{
		{
			name:    "minimal construction",
			options: []HTTPEndpointOption{},
		},
		{
			name: "with header",
			options: []HTTPEndpointOption{
				WithHeader(http.Header{
					"X-Test": []string{"value"},
				}),
			},
		},
		{
			name: "with payload",
			options: []HTTPEndpointOption{
				WithPayload([]byte(`{"key":"value"}`)),
			},
		},
		{
			name: "with transport control",
			options: []HTTPEndpointOption{
				WithTransportControl(&TransportControl{
					AddrPort:      netip.AddrPort{},
					TLSEnabled:    false,
					SkipTLSVerify: true,
				}),
			},
		},
		{
			name: "with everything",
			options: []HTTPEndpointOption{
				WithHeader(http.Header{
					"X-Test": []string{"value"},
				}),
				WithPayload([]byte(`{"key":"value"}`)),
				WithTransportControl(&TransportControl{
					AddrPort:      netip.AddrPort{},
					TLSEnabled:    false,
					SkipTLSVerify: true,
				}),
			},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			endpoint := NewHTTPEndpoint(url, http.MethodPost, c.options...)
			require.NotNil(t, endpoint)

			assert.Equal(t, url, endpoint.URL)
			assert.Equal(t, http.MethodPost, endpoint.Method)
		})
	}

	t.Run("with everything validated", func(t *testing.T) {
		header := http.Header{
			"X-Test": []string{"value"},
		}
		id := xid.New().String()
		payload := []byte(`{"key":"value"}`)
		tc := &TransportControl{
			AddrPort:      netip.AddrPort{},
			TLSEnabled:    false,
			SkipTLSVerify: true,
		}
		endpoint := NewHTTPEndpoint(
			url,
			http.MethodPost,
			WithHeader(header),
			WithID(id),
			WithPayload(payload),
			WithReadFast(),
			WithTransportControl(tc),
		)
		require.NotNil(t, endpoint)

		assert.Equal(t, url, endpoint.URL)
		assert.Equal(t, http.MethodPost, endpoint.Method)
		assert.Equal(t, id, endpoint.ID)
		assert.Equal(t, header, endpoint.Header)
		assert.Equal(t, payload, endpoint.Payload)
		assert.True(t, endpoint.OptReadFast)
		assert.Equal(t, tc, endpoint.TransportControl)
	})
}
