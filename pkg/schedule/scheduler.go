package schedule

import (
	"sync"
	"time"

	"github.com/rs/zerolog/log"
)

// TODO: better package name???

// Scheduler manages task scheduling, using a worker pool to execture tasks based on their cadence.
type Scheduler struct {
	sync.RWMutex

	stopChannel chan bool        // Channel to signal stopping the scheduler
	taskChannel chan<- Task      // Channel to send tasks to the worker pool
	tasks       []*ScheduledTask // A slice to hold the scheduled tasks
}

// ScheduledTask represents a task that is scheduled for execution.
type ScheduledTask struct {
	Task     Task
	NextExec time.Time
	Cadence  time.Duration
}

// AddTask adds a Task to the Scheduler with the specified cadence.
func (s *Scheduler) AddTask(task Task) {
	log.Trace().Msgf("Adding task to scheduler with cadence %v", task.Cadence())
	next := time.Now().Add(task.Cadence())
	s.Lock()
	defer s.Unlock()
	s.tasks = append(s.tasks, &ScheduledTask{
		Task:     task,
		NextExec: next,
		Cadence:  task.Cadence(),
	})
}

// Start starts the Scheduler.
func (s *Scheduler) Start() {
	ticker := time.NewTicker(1 * time.Second)
	for {
		select {
		case <-ticker.C:
			log.Trace().Msg("Checking for tasks to execute")
			s.RLock()
			now := time.Now()
			for _, scheduledTask := range s.tasks {
				if now.After(scheduledTask.NextExec) {
					log.Trace().Msgf("Sending task to worker pool: %v", scheduledTask.Task)
					s.taskChannel <- scheduledTask.Task
					scheduledTask.NextExec = now.Add(scheduledTask.Cadence)
					// TODO: make NextExec depend on previous execution time, not current time?
				}
			}
			s.RUnlock()
		case <-s.stopChannel:
			ticker.Stop()
			return
		}
	}
}

// Stop signals the Scheduler to stop processing tasks and exit.
func (s *Scheduler) Stop() {
	log.Debug().Msg("Stopping scheduler")
	s.stopChannel <- true
}

// NewScheduler creates and returns a new Scheduler.
func NewScheduler(taskChan chan<- Task) *Scheduler {
	log.Debug().Msg("Creating new scheduler")
	s := &Scheduler{
		stopChannel: make(chan bool),
		taskChannel: taskChan,
		tasks:       []*ScheduledTask{},
	}
	return s
}
