package wadjit

import (
	"errors"
	"fmt"
	"time"

	"github.com/hashicorp/go-multierror"
	"github.com/jakobilobi/go-taskman"
	"github.com/rs/xid"
)

// Watcher is a watcher that sends HTTP requests and WS messages to endpoints, and then
// forwards the responses to a response channel.
type Watcher struct {
	id      string
	cadence time.Duration

	watcherTasks []WatcherTask

	doneChan chan struct{}
}

// Close closes the HTTP watcher.
func (w *Watcher) Close() error {
	// Signal that the watcher is done
	close(w.doneChan)

	// Close all WS connections
	var result *multierror.Error
	for i := range w.watcherTasks {
		err := w.watcherTasks[i].Close()
		if err != nil {
			result = multierror.Append(result, err)
		}
	}
	return result.ErrorOrNil()
}

// ID returns the ID of the Watcher.
func (w *Watcher) ID() string {
	return w.id
}

// Job returns a taskman.Job that executes the Watcher's tasks.
func (w *Watcher) Job() taskman.Job {
	tasks := make([]taskman.Task, 0, len(w.watcherTasks))
	for i := range w.watcherTasks {
		tasks = append(tasks, w.watcherTasks[i].Task())
	}
	// Create the job
	job := taskman.Job{
		ID:       w.id,
		Cadence:  w.cadence,
		NextExec: time.Now().Add(w.cadence),
		Tasks:    tasks,
	}
	return job
}

// Start sets up the Watcher to start listening for responses, and initializes its tasks.
func (w *Watcher) Start(responseChan chan WatcherResponse) error {
	var result *multierror.Error

	// If the response channel is nil, the watcher cannot function
	if responseChan == nil {
		result = multierror.Append(result, errors.New("response channel is nil"))
	}

	// Set up the Watcher's channels if they are nil
	if w.doneChan == nil {
		w.doneChan = make(chan struct{})
	}

	// Initialize the watcher tasks
	for i := range w.watcherTasks {
		err := w.watcherTasks[i].Initialize(w.id, responseChan)
		if err != nil {
			result = multierror.Append(result, err)
		}
	}

	return result.ErrorOrNil()
}

// Validate checks that the Watcher is valid for use in the Wadjit.
func (w *Watcher) Validate() error {
	if w == nil {
		return errors.New("watcher is nil")
	}

	var result *multierror.Error
	if w.id == "" {
		result = multierror.Append(result, errors.New("id must not be nil"))
	}
	if w.cadence <= 0 {
		result = multierror.Append(result, errors.New("cadence must be greater than 0"))
	}
	if len(w.watcherTasks) == 0 {
		result = multierror.Append(result, errors.New("watcherTasks must not be nil or empty"))
	}
	if w.doneChan == nil {
		result = multierror.Append(result, errors.New("doneChan must not be nil"))
	} else {
		select {
		case <-w.doneChan:
			result = multierror.Append(result, errors.New("doneChan must not be closed"))
		default:
			// doneChan is not closed
		}
	}

	for i := range w.watcherTasks {
		err := w.watcherTasks[i].Validate()
		if err != nil {
			result = multierror.Append(result, err)
		}
	}

	return result.ErrorOrNil()
}

// NewWatcher creates and validates a new Watcher. If a nil ID is set to the Watcher, a randomized
// ID will be generated.
func NewWatcher(
	id string,
	cadence time.Duration,
	tasks []WatcherTask,
) (*Watcher, error) {
	if id == "" {
		id = xid.New().String()
	}

	w := &Watcher{
		id:           id,
		cadence:      cadence,
		watcherTasks: tasks,
		doneChan:     make(chan struct{}),
	}

	if err := w.Validate(); err != nil {
		return nil, fmt.Errorf("invalid watcher initialization: %w", err)
	}

	return w, nil
}
