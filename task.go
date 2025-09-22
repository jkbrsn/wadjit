package wadjit

import (
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

// errorResponse is a helper to create a WatcherResponse with an error.
func errorResponse(err error, taskID, watcherID string, reqURL *url.URL) WatcherResponse {
	return WatcherResponse{
		TaskID:    taskID,
		WatcherID: watcherID,
		URL:       reqURL,
		Err:       err,
		Payload:   nil,
	}
}
