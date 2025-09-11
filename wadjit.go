package wadjit

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/jkbrsn/taskman"
)

// Wadjit is a struct that manages a collection of endpoint watchers.
type Wadjit struct {
	watchers    sync.Map // Key xid.ID to value Watcher
	taskManager *taskman.TaskManager

	respGatherChan chan WatcherResponse
	respExportChan chan WatcherResponse

	ctx    context.Context
	cancel context.CancelFunc

	closeErr  error
	closeOnce sync.Once // Ensures idempotency of the Close method
	closeWG   sync.WaitGroup
}

// AddWatcher adds a Watcher to the Wadjit, starting it in the process.
func (w *Wadjit) AddWatcher(watcher *Watcher) error {
	if err := watcher.Validate(); err != nil {
		return fmt.Errorf("error validating watcher: %v", err)
	}

	// Duplicate watcher ID check
	if _, loaded := w.watchers.LoadOrStore(watcher.ID, watcher); loaded {
		return fmt.Errorf("watcher with ID %q already exists", watcher.ID)
	}

	err := watcher.start(w.respGatherChan)
	if err != nil {
		return fmt.Errorf("error starting watcher: %v", err)
	}

	job := watcher.job()
	err = w.taskManager.ScheduleJob(job)
	if err != nil {
		return fmt.Errorf("error scheduling job: %v", err)
	}
	w.watchers.Store(watcher.ID, watcher)

	return nil
}

// AddWatchers adds multiple Watchers to the Wadjit, starting them in the process.
func (w *Wadjit) AddWatchers(watchers ...*Watcher) error {
	var errs error
	for _, watcher := range watchers {
		if err := w.AddWatcher(watcher); err != nil {
			errs = errors.Join(errs, err)
		}
	}
	return errs
}

// Clear stops and removes all watchers from the Wadjit instance while keeping it running. This
// function is not atomic with respect to other operations.
func (w *Wadjit) Clear() error {
	var errs error

	// Get all watcher IDs (avoid concurrent map iteration)
	ids := w.WatcherIDs()

	// Remove each watcher
	for _, id := range ids {
		err := w.RemoveWatcher(id)
		if err != nil {
			errs = errors.Join(errs, fmt.Errorf("error removing watcher %q: %w", id, err))
		}
	}

	return errs
}

// Close stops all Wadjit processes and closes the Wadjit.
func (w *Wadjit) Close() error {
	w.closeOnce.Do(func() {
		// 1. Cancel in-flight work
		w.cancel()

		// 2. Remove remaining watchers, stop tasks
		ids := w.WatcherIDs() // Get watcher IDs first to avoid concurrent map iteration
		var errs error
		for _, id := range ids {
			err := w.RemoveWatcher(id)
			if err != nil {
				errs = errors.Join(errs, err)
			}
		}
		w.taskManager.Stop()

		// 3. Shut down the input channels to prevent new work entering the system
		if w.respGatherChan != nil {
			close(w.respGatherChan)
		}

		// 4. Wait for goroutines to finish and stop writing
		w.closeWG.Wait()

		// 5. Close remaining channels
		if w.respExportChan != nil {
			close(w.respExportChan)
		}

		w.closeErr = errs
	})

	return w.closeErr
}

// Metrics returns metrics from the Wadjit's internal task manager.
func (w *Wadjit) Metrics() taskman.TaskManagerMetrics {
	return w.taskManager.Metrics()
}

// RemoveWatcher closes and removes a Watcher from the Wadjit.
func (w *Wadjit) RemoveWatcher(id string) error {
	watcher, ok := w.watchers.LoadAndDelete(id)
	if !ok {
		return fmt.Errorf("watcher with ID %s not found", id)
	}

	err := watcher.(*Watcher).close()
	if err != nil {
		return err
	}

	err = w.taskManager.RemoveJob(id)
	if err != nil {
		return fmt.Errorf("error removing watcher: %w", err)
	}

	return nil
}

// Responses returns a channel that will receive all watchers' responses. The channel will
// be closed when the Wadjit is closed. Note: Unless sends on the response channel are
// consumed, a block may occur.
func (w *Wadjit) Responses() <-chan WatcherResponse {
	return w.respExportChan
}

// WatcherIDs returns a slice of strings containing the IDs of all active watchers.
func (w *Wadjit) WatcherIDs() []string {
	var ids []string
	w.watchers.Range(func(key, value any) bool {
		ids = append(ids, key.(string))
		return true
	})
	return ids
}

// listenForResponses consumes the channel where Watchers send responses from their monitoring jobs
// and forwards those responses to the externally facing channel.
func (w *Wadjit) listenForResponses() {
	for {
		select {
		case resp, ok := <-w.respGatherChan:
			if !ok {
				return // Channel closed
			}
			if w.ctx.Err() != nil {
				return // Context cancelled
			}

			// Send the response to the external facing channel
			// TODO: consider adding Watcher response metrics here
			w.respExportChan <- resp
		case <-w.ctx.Done():
			return
		}
	}
}

// New creates, and returns a new Wadjit. Note: Unless sends on the response channel are consumed,
// a block may occur.
func New() *Wadjit {
	ctx, cancel := context.WithCancel(context.Background())
	w := &Wadjit{
		watchers:       sync.Map{},
		taskManager:    taskman.New(),
		respGatherChan: make(chan WatcherResponse, 512),
		respExportChan: make(chan WatcherResponse, 512),
		ctx:            ctx,
		cancel:         cancel,
	}

	w.closeWG.Add(1)
	go func() {
		w.listenForResponses()
		w.closeWG.Done()
	}()

	return w
}
