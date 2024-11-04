package scheduler

import (
	"container/heap"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog/log"
)

// Scheduler manages task scheduling, using a worker pool to execture tasks based on their cadence.
type Scheduler struct {
	sync.RWMutex

	newTaskChannel chan bool   // Channel to signal that new tasks have entered the queue
	stopChannel    chan bool   // Channel to signal stopping the scheduler
	taskChan       chan<- Task // Send-only channel to send tasks to the worker pool

	jobQueue PriorityQueue // A priority queue to hold the scheduled jobs

	stopOnce sync.Once
}

// ScheduledJob represents a group of tasks that are scheduled for execution.
// TODO: consider adding an option to execute when inserted
// TODO: consider adding support for one-hit jobs, either with immediate or delayed execution, and with automatic removal after execution
// TODO: consider adding an option to stop an individual task
// TODO: would we for any reason need a cancellation flag here?
type ScheduledJob struct {
	Tasks []Task

	Cadence  time.Duration
	ID       string
	NextExec time.Time

	index int // Index within the heap
}

// AddTask adds a Task to the Scheduler.
// Note: wrapper for AddJob to simplify adding single tasks.
func (s *Scheduler) AddTask(task Task, jobID string) {
	s.AddJob([]Task{task}, task.Cadence(), jobID)
}

// AddJob adds a job of N tasks to the Scheduler.
// Requirements: tasks must have a cadence greater than 0, jobID must be unique.
func (s *Scheduler) AddJob(tasks []Task, cadence time.Duration, jobID string) {
	// Jobs with cadence < 0 are ignored, as a negative cadence makes no sense
	// Jobs with cadence == 0 are ignored, as a zero cadence would continuously execute the job and risk overwhelming the worker pool
	if cadence <= 0 {
		log.Warn().Msgf("Ignoring job with ID '%s' and cadence %v, cadence must be greater than 0", jobID, cadence)
		return
	}
	// If no job ID is provided, generate a 12 char random ID
	if jobID == "" {
		jobID = strings.Split(uuid.New().String(), "-")[0]
	}
	log.Trace().Msgf("Adding job with %d tasks with group ID '%s' and cadence %v", len(tasks), jobID, cadence)

	// The job uses a copy of the tasks slice, to avoid unintended consequences if the original slice is modified
	job := &ScheduledJob{
		Tasks:    append([]Task(nil), tasks...),
		Cadence:  cadence,
		ID:       jobID,
		NextExec: time.Now().Add(cadence),
	}

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

// RemoveJob removes a job from the Scheduler.
func (s *Scheduler) RemoveJob(jobID string) {
	s.Lock()
	defer s.Unlock()

	// Find the job in the heap and remove it
	for i, job := range s.jobQueue {
		if job.ID == jobID {
			heap.Remove(&s.jobQueue, i)
			break
		}
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
					s.taskChan <- task
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
		taskChan:       taskChan,
		jobQueue:       make(PriorityQueue, 0),
	}
	heap.Init(&s.jobQueue)
	return s
}
