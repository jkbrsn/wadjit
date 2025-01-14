package wadjit

import (
	"sync"

	"github.com/jakobilobi/go-taskman"
)

type Wadjit struct {
	endpoints   sync.Map // Key xid.ID to value Endpoint
	taskManager *taskman.TaskManager

	endpointChan chan Endpoint
	doneChan     chan struct{}
}

func (w *Wadjit) AddEndpoint(e Endpoint) {
	w.endpointChan <- e
}

func (w *Wadjit) Stop() {
	close(w.doneChan)
}

func (w *Wadjit) run() {
	for {
		select {
		case e := <-w.endpointChan:
			w.endpoints.Store(e.ID, e)
		case <-w.doneChan:
			return
		}
	}
}

func New() *Wadjit {
	w := &Wadjit{
		endpoints:    sync.Map{},
		taskManager:  taskman.New(),
		endpointChan: make(chan Endpoint),
		doneChan:     make(chan struct{}),
	}
	go w.run()
	return w
}
