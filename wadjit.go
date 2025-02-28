package wadjit

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/jakobilobi/go-taskman"
)

// Wadjit is a struct that manages a collection of endpoint watchers.
type Wadjit struct {
	watchers    sync.Map // Key xid.ID to value Watcher
	taskManager *taskman.TaskManager

	newWatcherChan chan *Watcher
	respGatherChan chan WatcherResponse
	respExportChan chan WatcherResponse
	wadjitStarted  chan struct{} // Blocks Watchers from initializing until Start is called

	ctx    context.Context
	cancel context.CancelFunc
}

// AddWatcher adds a Watcher to the Wadjit.
// Note: unless Start has been called, added Watchers will not start their tasks.
func (w *Wadjit) AddWatcher(watcher *Watcher) error {
	if err := watcher.Validate(); err != nil {
		return fmt.Errorf("error validating watcher: %v", err)
	}
	w.newWatcherChan <- watcher
	return nil
}

// AddWatchers adds multiple Watchers to the Wadjit.
// Note: unless Start has been called, added Watchers will not start their tasks.
func (w *Wadjit) AddWatchers(watchers ...*Watcher) error {
	var errs error
	for _, watcher := range watchers {
		if err := w.AddWatcher(watcher); err != nil {
			errs = errors.Join(errs, err)
		}
	}
	return errs
}

// Close stops all Wadjit processes and closes the Wadjit.
func (w *Wadjit) Close() {
	w.cancel()
	w.taskManager.Stop()

	w.watchers.Range(func(key, value any) bool {
		watcher := value.(*Watcher)
		watcher.close()
		return true
	})
}

// RemoveWatcher removes a Watcher from the Wadjit.
func (w *Wadjit) RemoveWatcher(id string) error {
	watcher, ok := w.watchers.LoadAndDelete(id)
	if !ok {
		return fmt.Errorf("watcher with ID %s not found", id)
	}

	err := watcher.(*Watcher).close()
	if err != nil {
		return err
	}

	w.taskManager.RemoveJob(id)

	return nil
}

// Start starts the Wadjit by unblocking Watcher initialization. After calling this function it
// is assumed that responsed sent on the returned channel will be consumed, and that failing to
// do so might produce a block.
func (w *Wadjit) Start() <-chan WatcherResponse {
	w.wadjitStarted <- struct{}{}
	return w.respExportChan
}

// listenForResponses consumes the channel where Watchers send responses from their monitoring jobs
// and forwards those responses to the externally facing channel.
func (w *Wadjit) listenForResponses() {
	for {
		select {
		case response := <-w.respGatherChan:
			// Send the response to the external facing channel
			// TODO: consider adding Watcher response metrics here
			w.respExportChan <- response
		case <-w.ctx.Done():
			return
		}
	}
}

// listenForWatchers consumes Watchers sent on the newWatcherChan and starts the Jobs defined by
// them when a Watcher is received. Blocks until the caller starts the Wadjit.
func (w *Wadjit) listenForWatchers() {
	<-w.wadjitStarted
	for {
		select {
		case watcher := <-w.newWatcherChan:
			err := watcher.start(w.respGatherChan)
			if err != nil {
				fmt.Printf("error starting watcher: %v\n", err)
				continue
			}
			job := watcher.job()
			err = w.taskManager.ScheduleJob(job)
			if err != nil {
				fmt.Printf("error scheduling job: %v\n", err)
				continue
			}
			w.watchers.Store(watcher.ID, watcher)
		case <-w.ctx.Done():
			return
		}
	}
}

// New creates, and returns a new Wadjit. To start the Wadjit, a separate call to Start is needed.
func New() *Wadjit {
	ctx, cancel := context.WithCancel(context.Background())
	w := &Wadjit{
		watchers:       sync.Map{},
		taskManager:    taskman.New(),
		newWatcherChan: make(chan *Watcher, 16),
		respGatherChan: make(chan WatcherResponse, 512),
		respExportChan: make(chan WatcherResponse, 512),
		wadjitStarted:  make(chan struct{}),
		ctx:            ctx,
		cancel:         cancel,
	}

	go w.listenForResponses()
	go w.listenForWatchers()

	return w
}
