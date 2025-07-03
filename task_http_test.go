package wadjit

import (
	"net/http"
	"net/url"
	"testing"
	"time"

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
		// Reload metadata to get updated timings
		metadata = resp.Metadata()
		assert.Greater(t, *metadata.TimeData.DataTransfer, time.Duration(0))
		assert.Greater(t, metadata.TimeData.RequestTimeTotal, time.Duration(0))
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
