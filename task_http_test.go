package wadjit

import (
	"errors"
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
					WithTLSSkipVerify(),
					WithDNSPolicy(DNSPolicy{
						Mode:       DNSRefreshStatic,
						StaticAddr: realAddr.AddrPort(),
					}),
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

				respCh := make(chan WatcherResponse, 1)
				ep := NewHTTPEndpoint(
					u,
					http.MethodGet,
					WithID("static"),
					WithDNSPolicy(DNSPolicy{
						Mode:       DNSRefreshStatic,
						StaticAddr: realAddr.AddrPort(),
					}),
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
		{
			name: "dns failure with fallback - error propagates",
			run: func(t *testing.T) {
				server := httptest.NewServer(http.HandlerFunc(echoHandler))
				defer server.Close()

				u, err := url.Parse(server.URL)
				require.NoError(t, err)

				realAddr, ok := server.Listener.Addr().(*net.TCPAddr)
				require.True(t, ok)

				// Create resolver that will succeed first, then fail
				resolver := newTestResolver(
					[]netip.Addr{realAddr.AddrPort().Addr()}, 20*time.Millisecond)
				policy := DNSPolicy{
					Mode:     DNSRefreshTTL,
					TTLMin:   15 * time.Millisecond,
					TTLMax:   50 * time.Millisecond,
					Resolver: resolver,
				}

				respCh := make(chan WatcherResponse, 2)
				ep := NewHTTPEndpoint(
					u,
					http.MethodGet,
					WithID("fallback-test"),
					WithReadFast(),
					WithDNSPolicy(policy),
				)

				require.NoError(t, ep.Initialize("fallback-watcher", respCh))

				// First execution - should succeed and establish cache
				require.NoError(t, ep.Task().Execute())
				resp1 := <-respCh
				require.NoError(t, resp1.Err)

				// Wait for TTL to expire
				time.Sleep(25 * time.Millisecond)

				// Make DNS fail
				resolver.SetError(errors.New("dns lookup failed"))

				// Second execution - should return error
				err = ep.Task().Execute()
				require.Error(t, err, "Execute should return DNS error")
				assert.ErrorContains(t, err, "dns lookup failed")

				select {
				case resp2 := <-respCh:
					// Error should be reported in response
					require.Error(t, resp2.Err)
					assert.ErrorContains(t, resp2.Err, "dns lookup failed")

					// But metadata should show fallback was used
					md := resp2.Metadata()
					require.NotNil(t, md.DNS)
					assert.True(t, md.DNS.FallbackUsed)
					require.NotNil(t, md.DNS.Err)
					assert.ErrorContains(t, md.DNS.Err, "dns lookup failed")
					assert.Equal(t, realAddr.AddrPort().Addr(), md.DNS.ResolvedAddrs[0])
				case <-time.After(time.Second):
					t.Fatal("timeout waiting for response")
				}
			},
		},
		{
			name: "dns failure without cache - hard failure",
			run: func(t *testing.T) {
				server := httptest.NewServer(http.HandlerFunc(echoHandler))
				defer server.Close()

				u, err := url.Parse(server.URL)
				require.NoError(t, err)

				// Create resolver that fails immediately
				resolver := newTestResolver(nil, 0)
				resolver.SetError(errors.New("dns resolution failed"))

				policy := DNSPolicy{
					Mode:     DNSRefreshSingleLookup,
					Resolver: resolver,
				}

				respCh := make(chan WatcherResponse, 1)
				ep := NewHTTPEndpoint(
					u,
					http.MethodGet,
					WithID("no-cache-test"),
					WithReadFast(),
					WithDNSPolicy(policy),
				)

				require.NoError(t, ep.Initialize("no-cache-watcher", respCh))

				// First execution with no cache should fail
				err = ep.Task().Execute()
				require.Error(t, err, "Execute should return DNS error")
				assert.ErrorContains(t, err, "dns resolution failed")

				select {
				case resp := <-respCh:
					// Error should be reported
					require.Error(t, resp.Err)
					assert.ErrorContains(t, resp.Err, "dns resolution failed")

					// Metadata should show DNS error with no fallback
					md := resp.Metadata()
					require.NotNil(t, md.DNS)
					assert.False(t, md.DNS.FallbackUsed)
					require.NotNil(t, md.DNS.Err)
					assert.ErrorContains(t, md.DNS.Err, "dns resolution failed")
					assert.Empty(t, md.DNS.ResolvedAddrs)
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
			name: "with static dns policy",
			options: []HTTPEndpointOption{
				WithDNSPolicy(DNSPolicy{Mode: DNSRefreshStatic,
					StaticAddr: netip.MustParseAddrPort("192.0.2.50:8080"),
				}),
			},
			expectedMode: DNSRefreshStatic,
		},
		{
			name: "with everything",
			options: []HTTPEndpointOption{
				WithHeader(http.Header{
					"X-Test": []string{"value"},
				}),
				WithPayload([]byte(`{"key":"value"}`)),
				WithDNSPolicy(DNSPolicy{Mode: DNSRefreshDefault}),
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
			WithDNSPolicy(DNSPolicy{Mode: DNSRefreshStatic, StaticAddr: staticAddr}),
			WithTLSSkipVerify(),
		)
		require.NotNil(t, endpoint)

		assert.Equal(t, testURL, endpoint.URL)
		assert.Equal(t, http.MethodPost, endpoint.Method)
		assert.Equal(t, id, endpoint.ID)
		assert.Equal(t, header, endpoint.Header)
		assert.Equal(t, payload, endpoint.Payload)
		assert.True(t, endpoint.OptReadFast)
		assert.True(t, endpoint.tlsSkipVerify)
	})
}

func TestHTTPEndpointTimeouts(t *testing.T) {
	t.Run("endpoint with timeouts initializes correctly", func(t *testing.T) {
		testURL, _ := url.Parse("http://example.com")
		timeouts := HTTPTimeouts{
			Total:          10 * time.Second,
			ResponseHeader: 5 * time.Second,
			IdleConn:       30 * time.Second,
			TLSHandshake:   10 * time.Second,
			Dial:           5 * time.Second,
		}

		endpoint := NewHTTPEndpoint(
			testURL,
			http.MethodGet,
			WithHTTPTimeouts(timeouts),
		)

		assert.True(t, endpoint.timeoutsSet)
		assert.Equal(t, timeouts, endpoint.timeouts)

		// Initialize the endpoint
		responseChan := make(chan WatcherResponse)
		err := endpoint.Initialize("watcher-id", responseChan)
		assert.NoError(t, err)

		// Verify client timeout is set
		assert.Equal(t, timeouts.Total, endpoint.client.Timeout)

		// Verify transport timeouts are set
		transport := endpoint.client.Transport.(*policyTransport).base
		assert.Equal(t, timeouts.ResponseHeader, transport.ResponseHeaderTimeout)
		assert.Equal(t, timeouts.IdleConn, transport.IdleConnTimeout)
		assert.Equal(t, timeouts.TLSHandshake, transport.TLSHandshakeTimeout)
	})

	t.Run("endpoint inherits default timeouts from Wadjit", func(t *testing.T) {
		defaultTimeouts := HTTPTimeouts{
			Total: 15 * time.Second,
			Dial:  7 * time.Second,
		}
		w := New(WithDefaultHTTPTimeouts(defaultTimeouts))
		defer func() {
			err := w.Close()
			assert.NoError(t, err)
		}()

		testURL, _ := url.Parse("http://example.com")
		endpoint := NewHTTPEndpoint(testURL, http.MethodGet)

		watcher := &Watcher{
			ID:      xid.New().String(),
			Cadence: time.Second,
			Tasks:   []WatcherTask{endpoint},
		}

		// Apply defaults
		w.applyDefaultHTTPTimeouts(watcher)

		assert.True(t, endpoint.timeoutsSet)
		assert.Equal(t, defaultTimeouts, endpoint.timeouts)
	})

	t.Run("explicit timeouts override Wadjit defaults", func(t *testing.T) {
		defaultTimeouts := HTTPTimeouts{
			Total: 15 * time.Second,
		}
		endpointTimeouts := HTTPTimeouts{
			Total: 5 * time.Second,
		}

		w := New(WithDefaultHTTPTimeouts(defaultTimeouts))
		defer func() {
			err := w.Close()
			assert.NoError(t, err)
		}()

		testURL, _ := url.Parse("http://example.com")
		endpoint := NewHTTPEndpoint(
			testURL,
			http.MethodGet,
			WithHTTPTimeouts(endpointTimeouts),
		)

		watcher := &Watcher{
			ID:      xid.New().String(),
			Cadence: time.Second,
			Tasks:   []WatcherTask{endpoint},
		}

		// Apply defaults (should not override explicit timeouts)
		w.applyDefaultHTTPTimeouts(watcher)

		assert.True(t, endpoint.timeoutsSet)
		assert.Equal(t, endpointTimeouts, endpoint.timeouts)
		assert.NotEqual(t, defaultTimeouts, endpoint.timeouts)
	})

	t.Run("invalid timeouts fail during initialization", func(t *testing.T) {
		testURL, _ := url.Parse("http://example.com")
		invalidTimeouts := HTTPTimeouts{
			Dial: -1 * time.Second, // Invalid negative dial timeout
		}

		endpoint := NewHTTPEndpoint(
			testURL,
			http.MethodGet,
			WithHTTPTimeouts(invalidTimeouts),
		)

		responseChan := make(chan WatcherResponse)
		err := endpoint.Initialize("watcher-id", responseChan)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "invalid HTTP timeouts")
	})

	t.Run("timeout causes request to fail", func(t *testing.T) {
		// Create a server that delays response
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			time.Sleep(200 * time.Millisecond) // Delay longer than timeout
			_, _ = w.Write([]byte("delayed response"))
		}))
		defer server.Close()

		testURL, _ := url.Parse(server.URL)
		timeouts := HTTPTimeouts{
			Total: 50 * time.Millisecond, // Very short timeout
		}

		endpoint := NewHTTPEndpoint(
			testURL,
			http.MethodGet,
			WithHTTPTimeouts(timeouts),
		)

		responseChan := make(chan WatcherResponse, 1)
		err := endpoint.Initialize("watcher-id", responseChan)
		require.NoError(t, err)

		// Execute request
		task := endpoint.Task()
		err = task.Execute()

		// Should timeout - Execute returns the error
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "Timeout")

		// Check response channel also has the timeout error
		select {
		case resp := <-responseChan:
			assert.NotNil(t, resp.Err)
			assert.Contains(t, resp.Err.Error(), "Timeout")
		case <-time.After(time.Second):
			t.Fatal("expected response on channel")
		}
	})
}
