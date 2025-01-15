package wadjit

import (
	"sync"

	"github.com/jakobilobi/go-taskman"
)

type Wadjit struct {
	watchers    sync.Map // Key xid.ID to value Watcher
	taskManager *taskman.TaskManager

	watcherChan chan Watcher
	doneChan    chan struct{}
}

func (w *Wadjit) AddWatcher(watcher Watcher) {
	w.watcherChan <- watcher
}

func (w *Wadjit) Stop() {
	close(w.doneChan)
}

func (w *Wadjit) run() {
	for {
		select {
		case watcher := <-w.watcherChan:
			w.watchers.Store(watcher.ID(), watcher)
		case <-w.doneChan:
			return
		}
	}
}

func New() *Wadjit {
	w := &Wadjit{
		watchers:    sync.Map{},
		taskManager: taskman.New(),
		watcherChan: make(chan Watcher),
		doneChan:    make(chan struct{}),
	}
	go w.run()
	return w
}
