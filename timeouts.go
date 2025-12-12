package wadjit

import (
	"errors"
	"time"
)

// HTTPTimeouts configures various timeout values for HTTP requests.
// Zero values indicate no timeout (except where Go stdlib provides defaults).
type HTTPTimeouts struct {
	// Total is the overall timeout for the entire request, including connection establishment,
	// redirects, and reading the response body. Maps to http.Client.Timeout.
	// Zero means no timeout.
	Total time.Duration

	// ResponseHeader is the timeout waiting for the server's response headers after the request
	// has been written. Maps to http.Transport.ResponseHeaderTimeout.
	// Zero means no timeout.
	ResponseHeader time.Duration

	// IdleConn is the maximum duration an idle connection will remain in the connection pool.
	// Maps to http.Transport.IdleConnTimeout.
	// Zero means no timeout (connections can remain idle indefinitely).
	IdleConn time.Duration

	// TLSHandshake is the maximum duration waiting for a TLS handshake to complete.
	// Maps to http.Transport.TLSHandshakeTimeout.
	// Zero uses the Go stdlib default (10 seconds as of Go 1.23).
	TLSHandshake time.Duration

	// Dial is the maximum duration waiting for a network dial to complete.
	// Applied to net.Dialer.Timeout.
	// Zero uses wadjit's default (5 seconds). Negative values are invalid.
	Dial time.Duration
}

// Validate checks that the HTTPTimeouts configuration is valid.
func (t HTTPTimeouts) Validate() error {
	if t.Dial < 0 {
		return errors.New("HTTPTimeouts.Dial cannot be negative")
	}
	return nil
}

// WSTimeouts configures various timeout values for WebSocket connections.
type WSTimeouts struct {
	// Handshake is the timeout for the WebSocket handshake.
	// Maps to websocket.Dialer.HandshakeTimeout.
	// Zero uses the websocket library default (45 seconds).
	Handshake time.Duration

	// Read is the deadline for reading messages from the WebSocket connection.
	// Applied before each ReadMessage call via SetReadDeadline.
	// Zero means no read deadline (reads can block indefinitely).
	Read time.Duration

	// Write is the deadline for writing messages to the WebSocket connection.
	// Applied before each WriteMessage call via SetWriteDeadline.
	// Zero means no write deadline (writes can block indefinitely).
	Write time.Duration

	// PingInterval is the interval for sending ping messages on persistent connections.
	// Only applies to PersistentJSONRPC mode. Zero disables automatic pings.
	// Must be less than Read timeout if both are set.
	PingInterval time.Duration
}

// Validate checks that the WSTimeouts configuration is valid.
func (t WSTimeouts) Validate() error {
	if t.PingInterval > 0 && t.Read > 0 && t.PingInterval >= t.Read {
		return errors.New("WSTimeouts.PingInterval must be less than WSTimeouts.Read when both are set")
	}
	return nil
}
