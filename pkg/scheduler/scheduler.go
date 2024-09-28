package scheduler

import (
	"sync"
	"sync/atomic"
	"time"

	"github.com/rs/zerolog/log"
)

// TODO: better package name???

// Scheduler manages task scheduling, using a worker pool to execture tasks based on their cadence.
type Scheduler struct {
	sync.RWMutex

	stopChannel chan bool                      // Channel to signal stopping the scheduler
	taskChannel chan<- Task                    // Channel to send tasks to the worker pool
	tasks       []*ScheduledTask               // A slice to hold the scheduled tasks
	taskGroups  map[string]*ScheduledTaskGroup // A map to hold task groups
}

// ScheduledTask represents a task that is scheduled for execution.
type ScheduledTask struct {
	Cadence     time.Duration
	NextExec    time.Time
	Task        Task
	TaskGroupID string
}

type ScheduledTaskGroup struct {
	ID        string
	TaskCount atomic.Int32
	waitGroup sync.WaitGroup
	ready     chan struct{}
}

// AddTask adds a Task to the Scheduler with the specified cadence.
// TODO: rename do AddSingleTask
func (s *Scheduler) AddTask(task Task) {
	log.Trace().Msgf("Adding task to scheduler with cadence %v", task.Cadence())
	s.Lock()
	defer s.Unlock()
	s.tasks = append(s.tasks, &ScheduledTask{
		Task:     task,
		NextExec: time.Now().Add(task.Cadence()),
		Cadence:  task.Cadence(),
	})
}

func (s *Scheduler) AddTaskToGroup(task Task, groupID string) {
	log.Trace().Msgf("Adding task to scheduler task group %s with cadence %v", groupID, task.Cadence())
	s.Lock()
	defer s.Unlock()
	group, ok := s.taskGroups[groupID]
	if !ok {
		group = &ScheduledTaskGroup{
			ID:    groupID,
			ready: make(chan struct{}),
		}
		s.taskGroups[groupID] = group
	}
	group.TaskCount.Add(1)
	group.waitGroup.Add(1)
	s.tasks = append(s.tasks, &ScheduledTask{
		Cadence:     task.Cadence(),
		NextExec:    time.Now().Add(task.Cadence()), // TODO: find a way to sync cadence with other tasks already present in group
		Task:        task,
		TaskGroupID: groupID,
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
					// Send tasks to worker pool if due
					if scheduledTask.TaskGroupID == "" {
						log.Trace().Msgf("Sending single task to worker pool: %v", scheduledTask.Task)
						s.taskChannel <- scheduledTask.Task
						scheduledTask.NextExec = now.Add(scheduledTask.Cadence)
					} else {
						log.Trace().Msgf("Sending grouped task to worker pool: %v", scheduledTask.Task)
						group := s.taskGroups[scheduledTask.TaskGroupID]
						if group.TaskCount.Load() > 0 { // TODO: this check is redundant?
							// TODO: implement waitgroup / ready functionality to ensure simultaneous execution of all tasks in a group
							s.taskChannel <- scheduledTask.Task
							scheduledTask.NextExec = now.Add(scheduledTask.Cadence)
						}
					}
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
		taskGroups:  make(map[string]*ScheduledTaskGroup),
	}
	return s
}
