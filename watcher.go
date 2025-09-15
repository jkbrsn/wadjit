package wadjit

import (
	"errors"
	"fmt"
	"time"

	"github.com/jkbrsn/taskman"
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

	var errs error
	if w.ID == "" {
		errs = errors.Join(errs, errors.New("var ID must not be nil"))
	}
	if w.Cadence <= 0 {
		errs = errors.Join(errs, errors.New("var Cadence must be greater than 0"))
	}
	if len(w.Tasks) == 0 {
		errs = errors.Join(errs, errors.New("var Tasks must not be nil or empty"))
	}
	if w.doneChan == nil {
		errs = errors.Join(errs, errors.New("doneChan must not be nil"))
	} else {
		select {
		case <-w.doneChan:
			errs = errors.Join(errs, errors.New("doneChan must not be closed"))
		default:
			// doneChan is not closed
		}
	}

	for i := range w.Tasks {
		err := w.Tasks[i].Validate()
		if err != nil {
			errs = errors.Join(errs, err)
		}
	}

	return errs
}

// Close closes the HTTP watcher.
func (w *Watcher) close() error {
	// Signal that the watcher is done
	close(w.doneChan)

	// Close all WS connections
	var errs error
	for i := range w.Tasks {
		err := w.Tasks[i].Close()
		if err != nil {
			errs = errors.Join(errs, err)
		}
	}
	return errs
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
	var errs error

	// If the response channel is nil, the watcher cannot function
	if responseChan == nil {
		errs = errors.Join(errs, errors.New("response channel is nil"))
	}

	// Set up the Watcher's channels if they are nil
	if w.doneChan == nil {
		w.doneChan = make(chan struct{})
	}

	// Initialize the watcher tasks
	for i := range w.Tasks {
		err := w.Tasks[i].Initialize(w.ID, responseChan)
		if err != nil {
			errs = errors.Join(errs, err)
		}
	}

	return errs
}

// NewWatcher creates and validates a new Watcher. If a nil ID is set to the Watcher, a randomized
// ID will be generated.
func NewWatcher(
	id string,
	cadence time.Duration,
	tasks []WatcherTask,
) (*Watcher, error) {
	newID := id
	if id == "" {
		newID = xid.New().String()
	}

	w := &Watcher{
		ID:       newID,
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
