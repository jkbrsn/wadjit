package scheduling

import (
	"sync"
	"time"

	"github.com/rs/zerolog/log"
)

type CadenceManager struct {
	tasks map[string]*Task // Map for quick task lookup
	mu    sync.RWMutex     // Mutex for thread-safe operations
	pool  *WorkerPool      // Reference to the worker pool
}

// refreshTasksPeriodically fetches updated task configs and updates the task map.
// TODO: implement
func (cm *CadenceManager) refreshTasksPeriodically() {
	for {
		log.Debug().Msg("Refreshing tasks...")
		// Placeholder: Fetch updated task configs from the endpoint DB
		// For each task:
		// - If new, add to cm.tasks and schedule its execution
		// - If existing but updated, update the task in cm.tasks
		// - Use a ticker or similar mechanism to handle task scheduling based on cadence

		// Placeholder: Example of triggering simultaneous execution of tasks
		/* tasks := []Task{} // Tasks belonging to the same group
		taskGroup := NewTaskGroup(tasks)
		cm.pool.SubmitGroup(taskGroup)
		taskGroup.ExecuteTogether() // Trigger simultaneous execution */

		time.Sleep(1 * time.Minute) // TODO: make this refresh rate configurable
	}
}

// AddOrUpdateTask adds or updates a task
// TODO: better name, implement scheduling logic
func (cm *CadenceManager) AddOrUpdateTask(newTask *Task) {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	cm.tasks[newTask.ID] = newTask

	// Additional logic to schedule the task for execution based on its cadence etc.
}

// NewCadenceManager creates a new CadenceManager and starts the task refresh loop.
func NewCadenceManager(pool *WorkerPool) *CadenceManager {
	cm := &CadenceManager{
		tasks: make(map[string]*Task),
		pool:  pool,
	}
	go cm.refreshTasksPeriodically()
	return cm
}
