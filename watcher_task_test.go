package wadjit

import (
	"net/http"
	"net/url"
	"testing"

	"github.com/rs/xid"
	"github.com/stretchr/testify/assert"
)

func TestHTTPEndpointImplementsWatcherTask(t *testing.T) {
	var _ WatcherTask = &HTTPEndpoint{}
}

func TestWSConnnImplementsWatcherTask(t *testing.T) {
	var _ WatcherTask = &wsConn{}
}

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

func TestWSConnInitialize(t *testing.T) {
	server := echoServer()
	defer server.Close()

	wsURL := "ws" + server.URL[4:] + "/ws"
	url, err := url.Parse(wsURL)
	assert.NoError(t, err, "failed to parse URL")
	header := make(http.Header)
	responseChan := make(chan WatcherResponse)

	conn := &wsConn{
		URL:    url,
		Header: header,
	}

	assert.Equal(t, url, conn.URL)
	assert.Equal(t, header, conn.Header)
	assert.Nil(t, conn.respChan)

	err = conn.Initialize(xid.NilID(), responseChan)
	assert.NoError(t, err)
	assert.NotNil(t, conn.respChan)
	assert.NotNil(t, conn.conn)
	assert.NotNil(t, conn.writeChan)
	assert.NotNil(t, conn.ctx)
	assert.NotNil(t, conn.cancel)
}

func TestWSConnReconnect(t *testing.T) {
	server := echoServer()
	defer server.Close()

	wsURL := "ws" + server.URL[4:] + "/ws"
	url, err := url.Parse(wsURL)
	assert.NoError(t, err, "failed to parse URL")
	header := make(http.Header)
	responseChan := make(chan WatcherResponse)

	conn := &wsConn{
		URL:    url,
		Header: header,
	}

	err = conn.Initialize(xid.NilID(), responseChan)
	assert.NoError(t, err)

	err = conn.connect()
	assert.Error(t, err)

	err = conn.reconnect()
	assert.NoError(t, err)
}
