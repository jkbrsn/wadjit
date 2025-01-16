package wadjit

import (
	"fmt"
	"sync"

	"github.com/jakobilobi/go-taskman"
)

// Wadjit is a struct that manages a collection of endpoint watchers.
type Wadjit struct {
	watchers    sync.Map // Key xid.ID to value Watcher
	taskManager *taskman.TaskManager

	watcherChan  chan Watcher
	responseChan chan []byte
	doneChan     chan struct{}
}

// AddWatcher adds a watcher to the Wadjit.
func (w *Wadjit) AddWatcher(watcher Watcher) {
	// TODO: add validation; e.g. check unique ID, validate Job() etc.
	w.watcherChan <- watcher
}

// TODO: implement RemoveWatcher

// Stop stops the Wadjit.
func (w *Wadjit) Stop() {
	close(w.doneChan)
	// TODO: stop all watchers
}

func (w *Wadjit) listenForResponses() {
	for {
		select {
		case response := <-w.responseChan:
			// TODO: this is a placeholder; implement response handling that can be
			//       handed over to the owner of the Wadjit
			fmt.Printf("response: %s\n", response)
		case <-w.doneChan:
			return
		}
	}
}

func (w *Wadjit) listenForWatchers() {
	for {
		select {
		case watcher := <-w.watcherChan:
			watcher.SetUp()
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
		watchers:    sync.Map{},
		taskManager: taskman.New(),
		watcherChan: make(chan Watcher),
		doneChan:    make(chan struct{}),
	}

	go w.listenForResponses()
	go w.listenForWatchers()

	return w
}
