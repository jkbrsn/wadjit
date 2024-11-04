package scheduler

import (
	"container/heap"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rs/zerolog/log"
)

// Scheduler manages task scheduling, using a worker pool to execture tasks based on their cadence.
type Scheduler struct {
	sync.RWMutex

	newTaskChannel chan bool   // Channel to signal that new tasks have entered the queue
	stopChannel    chan bool   // Channel to signal stopping the scheduler
	taskChannel    chan<- Task // Send channel, used to send tasks to the worker pool, TODO: rename to workerPoolChannel???

	jobQueue PriorityQueue // A priority queue to hold the scheduled jobs

	stopOnce sync.Once
}

// ScheduledJob represents a group of tasks that are scheduled for execution.
// TODO: consider adding an option to execute when inserted
// TODO: consider adding an option to stop the group from further execution, without stopping the scheduler
type ScheduledJob struct {
	Tasks []Task

	Cadence  time.Duration
	ID       string
	NextExec time.Time

	index int          // Index within the heap
	size  atomic.Int32 // Number of tasks in the group, TODO: consider removing
}

// AddTask adds a Task to the Scheduler.
// Note; gives the scheduled job the same ID as the single task.
func (s *Scheduler) AddTask(task Task, jobID string) {
	s.AddTasks([]Task{task}, task.Cadence(), jobID)
}

// AddTasks adds a group of Tasks to the Scheduler.
// TODO: assign random group ID if not provided?
func (s *Scheduler) AddTasks(tasks []Task, cadence time.Duration, jobID string) {
	// TODO: should we allow tasks with cadence == 0?
	if cadence <= 0 {
		log.Warn().Msgf("Ignoring job with ID '%s' and cadence %v, cadence must be greater than 0", jobID, cadence)
		return
	}
	log.Trace().Msgf("Adding job with %d tasks with group ID '%s' and cadence %v", len(tasks), jobID, cadence)

	job := &ScheduledJob{
		Tasks: tasks,
		// TODO: make copy of tasks to remove reference to original slice?
		//Tasks:    append([]Task(nil), tasks...), // Creates a copy of tasks
		Cadence:  cadence,
		ID:       jobID,
		NextExec: time.Now().Add(cadence),
	}
	job.size.Store(int32(len(tasks)))

	// Push the job to the queue
	s.Lock()
	heap.Push(&s.jobQueue, job)
	s.Unlock()

	// Signal the scheduler to check for new tasks
	log.Trace().Msg("Signaling new job added")
	select {
	case s.newTaskChannel <- true:
	default:
		// Do nothing if no one is listening
	}
}

// Start starts the Scheduler.
// With this design, the Scheduler manages its own goroutine internally.
func (s *Scheduler) Start() {
	log.Info().Msg("Starting scheduler")
	go s.run()
}

// run runs the Scheduler.
// This function is intended to be run as a goroutine.
func (s *Scheduler) run() {
	for {
		s.Lock()
		if s.jobQueue.Len() == 0 {
			s.Unlock()
			select {
			case <-s.newTaskChannel:
				log.Trace().Msg("New task added, checking for next job")
				continue
			case <-s.stopChannel:
				return
			}
		} else {
			nextJob := s.jobQueue[0]
			now := time.Now()
			delay := nextJob.NextExec.Sub(now)
			if delay <= 0 {
				log.Debug().Msgf("Executing job %s", nextJob.ID)
				heap.Pop(&s.jobQueue)
				s.Unlock()

				// Execute all tasks in the job
				for _, task := range nextJob.Tasks {
					s.taskChannel <- task
				}

				// Reschedule the job
				nextJob.NextExec = nextJob.NextExec.Add(nextJob.Cadence)
				s.Lock()
				heap.Push(&s.jobQueue, nextJob)
				s.Unlock()
				continue
			}
			s.Unlock()

			// Wait until the next job is due or until stopped.
			select {
			case <-time.After(delay):
				// Time to execute the next job
				continue
			case <-s.stopChannel:
				return
			}
		}
	}
}

// Stop signals the Scheduler to stop processing tasks and exit.
func (s *Scheduler) Stop() {
	log.Debug().Msg("Attempting scheduler stop")
	s.stopOnce.Do(func() {
		s.Lock()
		defer s.Unlock()

		select {
		case <-s.stopChannel:
			// Already closed
		default:
			close(s.stopChannel)
		}
	})
}

// NewScheduler creates and returns a new Scheduler.
func NewScheduler(taskChan chan<- Task) *Scheduler {
	log.Debug().Msg("Creating new scheduler")
	s := &Scheduler{
		newTaskChannel: make(chan bool),
		stopChannel:    make(chan bool),
		taskChannel:    taskChan,
		jobQueue:       make(PriorityQueue, 0),
	}
	heap.Init(&s.jobQueue)
	return s
}
