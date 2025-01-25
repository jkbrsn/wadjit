package wadjit

import (
	"fmt"
	"sync"

	"github.com/jakobilobi/go-taskman"
	"github.com/rs/xid"
)

// Wadjit is a struct that manages a collection of endpoint watchers.
type Wadjit struct {
	watchers    sync.Map // Key xid.ID to value Watcher
	taskManager *taskman.TaskManager

	newWatcherChan chan *Watcher
	wRespChan      chan WatcherResponse // TODO: pass pointer?
	userChan       chan WatcherResponse // TODO: pass pointer?
	doneChan       chan struct{}
	consumeStarted chan struct{} // Blocks until the caller starts consuming responses
}

// AddWatcher adds a watcher to the Wadjit.
// Note: unless the ResponseChannel is consumed, added Watchers will not be started.
func (w *Wadjit) AddWatcher(watcher *Watcher) error {
	if err := watcher.Validate(); err != nil {
		return fmt.Errorf("error validating watcher: %v", err)
	}
	w.newWatcherChan <- watcher
	return nil
}

// RemoveWatcher removes a watcher from the Wadjit.
func (w *Wadjit) RemoveWatcher(id xid.ID) error {
	watcher, ok := w.watchers.LoadAndDelete(id)
	if !ok {
		return fmt.Errorf("watcher with ID %s not found", id)
	}

	err := watcher.(*Watcher).Close()
	if err != nil {
		return err
	}

	w.taskManager.RemoveJob(id.String())

	return nil
}

// Close stops all Wadjit processes and closes the Wadjit.
func (w *Wadjit) Close() {
	close(w.doneChan)

	w.watchers.Range(func(key, value interface{}) bool {
		watcher := value.(*Watcher)
		watcher.Close()
		return true
	})
}

// ResponsesChannel returns the channel where responses from the internal Watcher instances
// are sent.
// Note: this method unblocks Watchers being added to the Wadjit.
func (w *Wadjit) ResponsesChannel() <-chan WatcherResponse {
	w.consumeStarted <- struct{}{}
	return w.userChan
}

func (w *Wadjit) listenForResponses() {
	for {
		select {
		case response := <-w.wRespChan:
			// Send the response to the external facing channel
			w.userChan <- response
		case <-w.doneChan:
			return
		}
	}
}

func (w *Wadjit) listenForWatchers() {
	// Block until the caller starts consuming responses
	<-w.consumeStarted
	for {
		select {
		case watcher := <-w.newWatcherChan:
			watcher.Start(w.wRespChan)
			job := watcher.Job()
			err := w.taskManager.ScheduleJob(job)
			if err != nil {
				fmt.Printf("error scheduling job: %v\n", err)
				continue
			}
			w.watchers.Store(watcher.ID(), watcher)
		case <-w.doneChan:
			return
		}
	}
}

// New creates, starts, and returns a new Wadjit.
func New() *Wadjit {
	w := &Wadjit{
		watchers:       sync.Map{},
		taskManager:    taskman.New(),
		newWatcherChan: make(chan *Watcher, 16),
		wRespChan:      make(chan WatcherResponse, 512),
		userChan:       make(chan WatcherResponse, 512),
		consumeStarted: make(chan struct{}),
		doneChan:       make(chan struct{}),
	}

	go w.listenForResponses()
	go w.listenForWatchers()

	return w
}
