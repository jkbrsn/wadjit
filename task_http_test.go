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

func TestHTTPEndpointImplementsWatcherTask(_ *testing.T) {
	var _ WatcherTask = &HTTPEndpoint{}
}

func TestHTTPEndpointInitialize(t *testing.T) {
	testURL, _ := url.Parse("http://example.com")
	header := make(http.Header)
	responseChan := make(chan WatcherResponse)

	endpoint := NewHTTPEndpoint(
		testURL,
		http.MethodGet,
		WithHeader(header),
		WithID("an-id"),
		WithDNSPolicy(DNSPolicy{Mode: DNSRefreshDefault}),
	)

	assert.Equal(t, testURL, endpoint.URL)
	assert.Equal(t, header, endpoint.Header)
	assert.Equal(t, "an-id", endpoint.ID)
	assert.Nil(t, endpoint.respChan)

	err := endpoint.Initialize("a-watcher-id", responseChan)
	assert.NoError(t, err)
	assert.NotNil(t, endpoint.respChan)
	assert.Equal(t, "a-watcher-id", endpoint.watcherID)
	assert.Equal(t, DNSRefreshDefault, endpoint.dnsPolicy.Mode)
}

func TestHTTPEndpointExecute(t *testing.T) {
	testCases := []struct {
		name string
		run  func(t *testing.T)
	}{
		{
			name: "basic post",
			run: func(t *testing.T) {
				server := httptest.NewServer(http.HandlerFunc(echoHandler))
				defer server.Close()

				parsedURL, err := url.Parse(server.URL)
				assert.NoError(t, err, "failed to parse HTTP URL")

				header := make(http.Header)
				header.Add("Content-Type", "application/json")
				responseChan := make(chan WatcherResponse, 1)

				endpoint := NewHTTPEndpoint(
					parsedURL,
					http.MethodPost,
					WithHeader(header),
					WithPayload([]byte(`{"key":"value"}`)),
					WithID("an-id"),
					WithDNSPolicy(DNSPolicy{Mode: DNSRefreshDefault}),
				)

				err = endpoint.Initialize("some-watcher-id", responseChan)
				assert.NoError(t, err)
				assert.Equal(t, DNSRefreshDefault, endpoint.dnsPolicy.Mode)

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
					metadata := resp.Metadata()
					assert.NotNil(t, metadata)
					assert.Equal(t, "application/json", metadata.Headers.Get("Content-Type"))
					assert.Greater(t, metadata.TimeData.Latency, time.Duration(0))
					assert.Greater(t, metadata.TimeData.ReceivedAt, metadata.TimeData.SentAt)
					assert.Nil(t, metadata.TimeData.DNSLookup)
					assert.Greater(t, *metadata.TimeData.TCPConnect, time.Duration(0))
					assert.Zero(t, metadata.TimeData.TLSHandshake)
					assert.Greater(t, *metadata.TimeData.ServerProcessing, time.Duration(0))
					assert.Nil(t, metadata.TimeData.DataTransfer)
					assert.Zero(t, metadata.TimeData.RequestTimeTotal)

					data, err := resp.Data()
					assert.NoError(t, err)
					assert.JSONEq(t, `{"key":"value"}`, string(data))

					origPath := endpoint.URL.Path
					resp.URL.Path = "/tampered"
					assert.NotEqual(t, resp.URL, endpoint.URL, "pointers should differ")
					assert.Equal(t, origPath, endpoint.URL.Path, "endpoint URL must stay unchanged")
					assert.Equal(t, "/tampered", resp.URL.Path)

					metadata = resp.Metadata()
					assert.Greater(t, *metadata.TimeData.DataTransfer, time.Duration(0))
					assert.Greater(t, *metadata.TimeData.RequestTimeTotal, time.Duration(0))
				case <-time.After(1 * time.Second):
					t.Fatal("timeout waiting for response")
				}
			},
		},
		{
			name: "static dial override bypasses DNS",
			run: func(t *testing.T) {
				server := httptest.NewServer(http.HandlerFunc(echoHandler))
				defer server.Close()

				u, err := url.Parse(server.URL)
				require.NoError(t, err)
				fakeHost := "nonexistent.example.com"
				u.Host = fakeHost

				realAddr, ok := server.Listener.Addr().(*net.TCPAddr)
				require.True(t, ok, "expected listener address to be *net.TCPAddr")

				ep := NewHTTPEndpoint(
					u,
					http.MethodGet,
					WithID("id"),
					WithStaticDialOverride(realAddr.AddrPort()),
					WithDNSPolicy(DNSPolicy{Mode: DNSRefreshDefault}),
				)
				responseChan := make(chan WatcherResponse, 1)
				require.NoError(t, ep.Initialize("wid", responseChan))
				require.Equal(t, DNSRefreshStatic, ep.dnsPolicy.Mode)

				go func() { _ = ep.Task().Execute() }()

				select {
				case resp := <-responseChan:
					assert.NoError(t, resp.Err)
					md := resp.Metadata()
					assert.Nil(t, md.TimeData.DNSLookup)
					serverAddr := net.TCPAddr{
						IP:   net.ParseIP(realAddr.IP.String()),
						Port: realAddr.Port,
					}
					assert.Equal(t, serverAddr.String(), resp.Metadata().RemoteAddr.String())
					assert.Equal(t,
						realAddr.AddrPort(), resp.Metadata().RemoteAddr.(*net.TCPAddr).AddrPort())
					assert.Equal(t, fakeHost, resp.URL.Hostname())
				case <-time.After(time.Second):
					t.Fatal("timeout waiting for response")
				}
			},
		},
		{
			name: "static dial override tls",
			run: func(t *testing.T) {
				server := httptest.NewTLSServer(http.HandlerFunc(echoHandler))
				defer server.Close()

				u, err := url.Parse(server.URL)
				require.NoError(t, err)
				u.Host = "geo.example.com"

				realAddr, ok := server.Listener.Addr().(*net.TCPAddr)
				require.True(t, ok, "expected listener address to be *net.TCPAddr")

				ep := NewHTTPEndpoint(
					u,
					http.MethodGet,
					WithID("tls-test"),
					WithStaticDialOverride(realAddr.AddrPort()),
					WithTLSSkipVerify(),
					WithDNSPolicy(DNSPolicy{Mode: DNSRefreshDefault}),
				)
				respCh := make(chan WatcherResponse, 1)
				require.NoError(t, ep.Initialize("wid", respCh))
				require.Equal(t, DNSRefreshStatic, ep.dnsPolicy.Mode)

				go func() { _ = ep.Task().Execute() }()

				select {
				case resp := <-respCh:
					assert.NoError(t, resp.Err)
					md := resp.Metadata()
					assert.Nil(t, md.TimeData.DNSLookup)
					assert.NotZero(t, *md.TimeData.TLSHandshake)
					assert.Equal(t,
						realAddr.AddrPort(), resp.Metadata().RemoteAddr.(*net.TCPAddr).AddrPort())
				case <-time.After(time.Second):
					t.Fatal("timeout")
				}
			},
		},
		{
			name: "dns policy static",
			run: func(t *testing.T) {
				server := httptest.NewServer(http.HandlerFunc(echoHandler))
				defer server.Close()

				u, err := url.Parse(server.URL)
				require.NoError(t, err)
				realAddr, ok := server.Listener.Addr().(*net.TCPAddr)
				require.True(t, ok, "expected listener address to be *net.TCPAddr")

				u.Host = "static.example.com"

				policy := DNSPolicy{Mode: DNSRefreshStatic, StaticAddr: realAddr.AddrPort()}
				respCh := make(chan WatcherResponse, 1)
				ep := NewHTTPEndpoint(
					u,
					http.MethodGet,
					WithID("static"),
					WithDNSPolicy(policy),
				)

				require.NoError(t, ep.Initialize("static-watcher", respCh))
				require.Equal(t, DNSRefreshStatic, ep.dnsPolicy.Mode)

				go func() { _ = ep.Task().Execute() }()

				select {
				case resp := <-respCh:
					require.NoError(t, resp.Err)
					md := resp.Metadata()
					require.NotNil(t, md)
					assert.Nil(t, md.TimeData.DNSLookup)
					received, ok := md.RemoteAddr.(*net.TCPAddr)
					require.True(t, ok)
					assert.Equal(t, realAddr.AddrPort(), received.AddrPort())
					require.NotNil(t, md.DNS)
					assert.Equal(t, DNSRefreshStatic, md.DNS.Mode)
					require.Len(t, md.DNS.ResolvedAddrs, 1)
					assert.Equal(t, realAddr.IP.String(), md.DNS.ResolvedAddrs[0].String())
				case <-time.After(time.Second):
					t.Fatal("timeout waiting for response")
				}
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, tc.run)
	}
}

func TestHTTPEndpointDNSRefreshSingleLookup(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(echoHandler))
	defer server.Close()

	u, err := url.Parse(server.URL)
	require.NoError(t, err)

	realAddr, ok := server.Listener.Addr().(*net.TCPAddr)
	require.True(t, ok, "expected listener address to be *net.TCPAddr")

	resolver := newTestResolver([]netip.Addr{realAddr.AddrPort().Addr()}, 30*time.Second)
	policy := DNSPolicy{Mode: DNSRefreshSingleLookup, Resolver: resolver}

	respCh := make(chan WatcherResponse, 2)
	ep := NewHTTPEndpoint(
		u,
		http.MethodGet,
		WithID("single-lookup"),
		WithReadFast(),
		WithDNSPolicy(policy),
	)

	require.NoError(t, ep.Initialize("watcher-single", respCh))
	require.Equal(t, DNSRefreshSingleLookup, ep.dnsPolicy.Mode)

	require.NoError(t, ep.Task().Execute())
	require.NoError(t, ep.Task().Execute())

	resp1 := <-respCh
	resp2 := <-respCh

	require.NoError(t, resp1.Err)
	require.NoError(t, resp2.Err)

	assert.Equal(t, 1, resolver.Calls(), "resolver should be invoked once for single lookup")

	md1 := resp1.Metadata()
	require.NotNil(t, md1.DNS)
	assert.Equal(t, DNSRefreshSingleLookup, md1.DNS.Mode)
	md2 := resp2.Metadata()
	require.NotNil(t, md2.DNS)
	assert.Equal(t, DNSRefreshSingleLookup, md2.DNS.Mode)

	require.NotNil(t, md1.RemoteAddr)
	require.NotNil(t, md2.RemoteAddr)
	assert.Equal(t, md1.RemoteAddr.String(), md2.RemoteAddr.String())
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
				WithDNSPolicy(DNSPolicy{Mode: DNSRefreshDefault}),
			)
			err = endpoint.Initialize("some-watcher-id", responseChan)
			assert.NoError(t, err)
			assert.Equal(t, DNSRefreshDefault, endpoint.dnsPolicy.Mode)

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
	ep := NewHTTPEndpoint(
		u,
		http.MethodGet,
		WithHeader(http.Header{}),
		WithID("id1"),
		WithDNSPolicy(DNSPolicy{Mode: DNSRefreshDefault}),
	)
	respChan := make(chan WatcherResponse, 1)
	require.NoError(t, ep.Initialize("watcher-1", respChan))
	assert.Equal(t, DNSRefreshDefault, ep.dnsPolicy.Mode)

	// Run one request synchronously
	task, ok := ep.Task().(*httpRequest)
	require.True(t, ok, "expected task to be *httpRequest")
	require.NoError(t, task.Execute())

	// Check the response metadata for RemoteAddr
	resp := <-respChan
	metadata := resp.Metadata()
	assert.NotNil(t, metadata)
	assert.Greater(t, metadata.TimeData.Latency, time.Duration(0))
	require.Equal(t, server.Listener.Addr(), metadata.RemoteAddr)
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
		WithDNSPolicy(DNSPolicy{Mode: DNSRefreshDefault}),
	)

	respCh := make(chan WatcherResponse, 1)
	require.NoError(t, ep.Initialize("wid", respCh))
	require.Equal(t, DNSRefreshDefault, ep.dnsPolicy.Mode)

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
	assert.Nil(t, md2.TimeData.TCPConnect,
		"second request should reuse connection (no TCP connect)")

	// Remote address must be the same for both requests.
	assert.Equal(t, md1.RemoteAddr.String(), md2.RemoteAddr.String())
}

func TestNewHTTPEndpoint(t *testing.T) {
	testURL, err := url.Parse("http://example.com")
	assert.NoError(t, err, "failed to parse URL")

	cases := []struct {
		name         string
		options      []HTTPEndpointOption
		expectedMode DNSRefreshMode
	}{
		{
			name:         "minimal construction",
			options:      []HTTPEndpointOption{},
			expectedMode: DNSRefreshDefault,
		},
		{
			name: "with header",
			options: []HTTPEndpointOption{
				WithHeader(http.Header{
					"X-Test": []string{"value"},
				}),
			},
			expectedMode: DNSRefreshDefault,
		},
		{
			name: "with payload",
			options: []HTTPEndpointOption{
				WithPayload([]byte(`{"key":"value"}`)),
			},
			expectedMode: DNSRefreshDefault,
		},
		{
			name: "with static dial override",
			options: []HTTPEndpointOption{
				WithStaticDialOverride(netip.MustParseAddrPort("192.0.2.50:8080")),
			},
			expectedMode: DNSRefreshDefault,
		},
		{
			name: "with everything",
			options: []HTTPEndpointOption{
				WithHeader(http.Header{
					"X-Test": []string{"value"},
				}),
				WithPayload([]byte(`{"key":"value"}`)),
				WithStaticDialOverride(netip.MustParseAddrPort("198.51.100.20:80")),
				WithTLSSkipVerify(),
			},
			expectedMode: DNSRefreshDefault,
		},
		{
			name: "with dns policy",
			options: []HTTPEndpointOption{
				WithDNSPolicy(DNSPolicy{Mode: DNSRefreshCadence, Cadence: time.Second}),
			},
			expectedMode: DNSRefreshCadence,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			endpoint := NewHTTPEndpoint(testURL, http.MethodPost, c.options...)
			require.NotNil(t, endpoint)

			assert.Equal(t, testURL, endpoint.URL)
			assert.Equal(t, http.MethodPost, endpoint.Method)
			assert.Equal(t, c.expectedMode, endpoint.dnsPolicy.Mode)
		})
	}

	t.Run("with everything validated", func(t *testing.T) {
		header := http.Header{
			"X-Test": []string{"value"},
		}
		id := xid.New().String()
		payload := []byte(`{"key":"value"}`)
		staticAddr := netip.MustParseAddrPort("192.0.2.60:8080")
		endpoint := NewHTTPEndpoint(
			testURL,
			http.MethodPost,
			WithHeader(header),
			WithID(id),
			WithPayload(payload),
			WithReadFast(),
			WithStaticDialOverride(staticAddr),
			WithTLSSkipVerify(),
		)
		require.NotNil(t, endpoint)

		assert.Equal(t, testURL, endpoint.URL)
		assert.Equal(t, http.MethodPost, endpoint.Method)
		assert.Equal(t, id, endpoint.ID)
		assert.Equal(t, header, endpoint.Header)
		assert.Equal(t, payload, endpoint.Payload)
		assert.True(t, endpoint.OptReadFast)
		assert.True(t, endpoint.hasStaticDialOverride)
		assert.Equal(t, staticAddr, endpoint.staticDialOverride)
		assert.True(t, endpoint.tlsSkipVerify)
	})
}
