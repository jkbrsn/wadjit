package wadjit

import (
	"sync"

	"github.com/jakobilobi/go-taskman"
)

type Wadjit struct {
	endpoints   sync.Map
	taskManager *taskman.TaskManager
}

func New() *Wadjit {
	return &Wadjit{
		endpoints:   sync.Map{},
		taskManager: taskman.New(),
	}
}
