package wadjit

import (
	"net/netip"
	"net/url"

	"github.com/jkbrsn/taskman"
)

// WatcherTask is a task that the Watcher can execute to interact with a target endpoint.
type WatcherTask interface {
	// Close closes the WatcherTask, cleaning up and releasing resources.
	// Note: will block until the task is closed. (?)
	Close() error

	// Initialize sets up the WatcherTask to be ready to watch an endpoint.
	Initialize(watcherID string, respChan chan<- WatcherResponse) error

	// Task returns a taskman.Task that sends requests and messages to the endpoint.
	Task() taskman.Task

	// Validate checks that the WatcherTask is ready for initialization.
	Validate() error
}

// TransportControl contains information about the transport layer of a connection.
type TransportControl struct {
	// A literal address to connect to.
	AddrPort netip.AddrPort
	// Set to true to negotiate a TLS connection after the TCP call.
	TLSEnabled bool
	// SkipTLSVerify disables validation of the server certificate when TLSEnabled is true.
	// Use with caution – intended mainly for tests or trusted internal endpoints.
	SkipTLSVerify bool
}

// errorResponse is a helper to create a WatcherResponse with an error.
func errorResponse(err error, taskID, watcherID string, url *url.URL) WatcherResponse {
	return WatcherResponse{
		TaskID:    taskID,
		WatcherID: watcherID,
		URL:       url,
		Err:       err,
		Payload:   nil,
	}
}
