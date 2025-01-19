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

	watcherChan  chan Watcher
	responseChan chan WatcherResponse
	doneChan     chan struct{}
}

// AddWatcher adds a watcher to the Wadjit.
func (w *Wadjit) AddWatcher(watcher Watcher) {
	// TODO: add validation; e.g. check unique ID, validate Job() etc.
	w.watcherChan <- watcher
}

// RemoveWatcher removes a watcher from the Wadjit.
func (w *Wadjit) RemoveWatcher(id xid.ID) error {
	watcher, ok := w.watchers.LoadAndDelete(id)
	if !ok {
		return fmt.Errorf("watcher with ID %s not found", id)
	}

	err := watcher.(Watcher).Close()
	if err != nil {
		return err
	}

	return nil
}

// Close stops all Wadjit processes and closes the Wadjit.
func (w *Wadjit) Close() {
	close(w.doneChan)

	w.watchers.Range(func(key, value interface{}) bool {
		watcher := value.(Watcher)
		watcher.Close()
		return true
	})
}

func (w *Wadjit) listenForResponses() {
	for {
		select {
		case response := <-w.responseChan:
			// TODO: this is a placeholder; implement response handling that can be
			//       handed over to the owner of the Wadjit
			fmt.Printf("response: %v\n", response)
		case <-w.doneChan:
			return
		}
	}
}

func (w *Wadjit) listenForWatchers() {
	for {
		select {
		case watcher := <-w.watcherChan:
			watcher.SetUp(w.responseChan)
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
		watchers:     sync.Map{},
		taskManager:  taskman.New(),
		watcherChan:  make(chan Watcher),         // TODO: currently blocks, make buffered?
		responseChan: make(chan WatcherResponse), // TODO: currently blocks, make buffered?
		doneChan:     make(chan struct{}),
	}

	go w.listenForResponses()
	go w.listenForWatchers()

	return w
}
