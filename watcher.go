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
	ID      string
	Cadence time.Duration
	Tasks   []WatcherTask

	doneChan chan struct{}
}

// Validate checks that the Watcher is valid for use in the Wadjit.
func (w *Watcher) Validate() error {
	if w == nil {
		return errors.New("watcher is nil")
	}

	var result *multierror.Error
	if w.ID == "" {
		result = multierror.Append(result, errors.New("var ID must not be nil"))
	}
	if w.Cadence <= 0 {
		result = multierror.Append(result, errors.New("var Cadence must be greater than 0"))
	}
	if len(w.Tasks) == 0 {
		result = multierror.Append(result, errors.New("var Tasks must not be nil or empty"))
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

	for i := range w.Tasks {
		err := w.Tasks[i].Validate()
		if err != nil {
			result = multierror.Append(result, err)
		}
	}

	return result.ErrorOrNil()
}

// Close closes the HTTP watcher.
func (w *Watcher) close() error {
	// Signal that the watcher is done
	close(w.doneChan)

	// Close all WS connections
	var result *multierror.Error
	for i := range w.Tasks {
		err := w.Tasks[i].Close()
		if err != nil {
			result = multierror.Append(result, err)
		}
	}
	return result.ErrorOrNil()
}

// job returns a taskman.Job that executes the Watcher's tasks.
func (w *Watcher) job() taskman.Job {
	tasks := make([]taskman.Task, 0, len(w.Tasks))
	for i := range w.Tasks {
		tasks = append(tasks, w.Tasks[i].Task())
	}
	// Create the job
	job := taskman.Job{
		ID:       w.ID,
		Cadence:  w.Cadence,
		NextExec: time.Now().Add(w.Cadence),
		Tasks:    tasks,
	}
	return job
}

// Start sets up the Watcher to start listening for responses, and initializes its tasks.
func (w *Watcher) start(responseChan chan WatcherResponse) error {
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
	for i := range w.Tasks {
		err := w.Tasks[i].Initialize(w.ID, responseChan)
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
		ID:       id,
		Cadence:  cadence,
		Tasks:    tasks,
		doneChan: make(chan struct{}),
	}

	if err := w.Validate(); err != nil {
		return nil, fmt.Errorf("invalid watcher initialization: %w", err)
	}

	return w, nil
}

// WatcherTasksToSlice is a helper function to get a slice of the WatcherTask interface from
// object types implementing it.
func WatcherTasksToSlice(tasks ...WatcherTask) []WatcherTask {
	var taskSlice []WatcherTask
	taskSlice = append(taskSlice, tasks...)
	return taskSlice
}
