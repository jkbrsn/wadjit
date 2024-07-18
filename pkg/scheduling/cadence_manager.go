package scheduling

import (
	"sync"
	"time"

	"github.com/rs/zerolog/log"
)

// CadenceManager manages task scheduling, using a worker pool to execture tasks based on their cadence.
type CadenceManager struct {
	tasks map[string]*Task // Map for quick task lookup
	mu    sync.RWMutex     // Mutex for thread-safe operations
	pool  *WorkerPool      // Reference to the worker pool
	quit  chan struct{}    // Channel to signal stopping
}

// scheduleTasks schedules tasks for execution based on their cadence.
func (cm *CadenceManager) scheduleTasks() {
    cm.mu.Lock()
    defer cm.mu.Unlock()

    now := time.Now()
    for _, task := range cm.tasks {
        if task.NextExec.Before(now) || task.NextExec.Equal(now) {
            cm.pool.Submit(*task) // Assume Submit accepts a Task value
            task.NextExec = now.Add(task.Cadence) // Schedule next run
        }
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

// RefreshTasksPeriodically fetches updated task configs and updates the task map.
// TODO: implement
func (cm *CadenceManager) RefreshTasksPeriodically() {
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

		time.Sleep(15 * time.Second) // TODO: make this refresh rate configurable
	}
}

// StartScheduling starts the task scheduling loop.
func (cm *CadenceManager) StartScheduling() {
    ticker := time.NewTicker(2 * time.Second) // TODO: experiment with this, make configurable
    go func() {
        for {
            select {
            case <-ticker.C:
				log.Trace().Msg("Scheduling tasks...")
                cm.scheduleTasks()
			case <-cm.quit:
				log.Debug().Msg("Stopping task scheduling...")
				ticker.Stop()
				return
            }
        }
    }()
}

// NewCadenceManager creates and returns a new CadenceManager.
func NewCadenceManager(pool *WorkerPool) *CadenceManager {
	cm := &CadenceManager{
		tasks: make(map[string]*Task),
		pool:  pool,
		quit:  make(chan struct{}),
	}
	return cm
}
