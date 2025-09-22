// Package wadjit provides a framework for monitoring HTTP and WebSocket endpoints.
package wadjit

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/jkbrsn/taskman"
	"github.com/rs/zerolog"
)

const (
	// defaultResponseChanBufferSize is the buffer size for response channels to prevent blocking
	defaultResponseChanBufferSize = 512
)

// Wadjit is a struct that manages a collection of endpoint watchers.
type Wadjit struct {
	watchers    sync.Map // Key xid.ID to value Watcher
	taskManager *taskman.TaskManager

	respGatherChan chan WatcherResponse
	respExportChan chan WatcherResponse

	defaultDNSPolicy    DNSPolicy
	hasDefaultDNSPolicy bool

	ctx    context.Context
	cancel context.CancelFunc

	closeErr  error
	closeOnce sync.Once // Ensures idempotency of the Close method
	closeWG   sync.WaitGroup
}

// AddWatcher adds a Watcher to the Wadjit, starting it in the process.
func (w *Wadjit) AddWatcher(watcher *Watcher) error {
	w.applyDefaultDNSPolicy(watcher)

	if err := watcher.Validate(); err != nil {
		return fmt.Errorf("error validating watcher: %v", err)
	}

	// Duplicate watcher ID check
	if _, loaded := w.watchers.LoadOrStore(watcher.ID, watcher); loaded {
		return fmt.Errorf("watcher with ID %q already exists", watcher.ID)
	}

	err := watcher.start(w.respGatherChan)
	if err != nil {
		return fmt.Errorf("error starting watcher: %v", err)
	}

	job := watcher.job()
	err = w.taskManager.ScheduleJob(job)
	if err != nil {
		return fmt.Errorf("error scheduling job: %v", err)
	}
	w.watchers.Store(watcher.ID, watcher)

	return nil
}

func (w *Wadjit) applyDefaultDNSPolicy(watcher *Watcher) {
	if !w.hasDefaultDNSPolicy || watcher == nil {
		return
	}

	for i := range watcher.Tasks {
		if endpoint, ok := watcher.Tasks[i].(*HTTPEndpoint); ok && endpoint != nil {
			if endpoint.dnsPolicySet {
				continue
			}
			endpoint.dnsPolicy = w.defaultDNSPolicy
			endpoint.dnsPolicySet = true
		}
	}
}

// AddWatchers adds multiple Watchers to the Wadjit, starting them in the process.
func (w *Wadjit) AddWatchers(watchers ...*Watcher) error {
	var errs error
	for _, watcher := range watchers {
		if err := w.AddWatcher(watcher); err != nil {
			errs = errors.Join(errs, err)
		}
	}
	return errs
}

// Clear stops and removes all watchers from the Wadjit instance while keeping it running. This
// function is not atomic with respect to other operations.
func (w *Wadjit) Clear() error {
	var errs error

	// Get all watcher IDs (avoid concurrent map iteration)
	ids := w.WatcherIDs()

	// Remove each watcher
	for _, id := range ids {
		err := w.RemoveWatcher(id)
		if err != nil {
			errs = errors.Join(errs, fmt.Errorf("error removing watcher %q: %w", id, err))
		}
	}

	return errs
}

// Close stops all Wadjit processes and closes the Wadjit.
func (w *Wadjit) Close() error {
	w.closeOnce.Do(func() {
		// 1. Cancel in-flight work
		w.cancel()

		// 2. Remove remaining watchers, stop tasks
		ids := w.WatcherIDs() // Get watcher IDs first to avoid concurrent map iteration
		var errs error
		for _, id := range ids {
			err := w.RemoveWatcher(id)
			if err != nil {
				errs = errors.Join(errs, err)
			}
		}
		w.taskManager.Stop()

		// 3. Shut down the input channels to prevent new work entering the system
		if w.respGatherChan != nil {
			close(w.respGatherChan)
		}

		// 4. Wait for goroutines to finish and stop writing
		w.closeWG.Wait()

		// 5. Close remaining channels
		if w.respExportChan != nil {
			close(w.respExportChan)
		}

		w.closeErr = errs
	})

	return w.closeErr
}

// Metrics returns metrics from the Wadjit's internal task manager.
func (w *Wadjit) Metrics() taskman.TaskManagerMetrics {
	return w.taskManager.Metrics()
}

// RemoveWatcher closes and removes a Watcher from the Wadjit.
func (w *Wadjit) RemoveWatcher(id string) error {
	watcher, ok := w.watchers.LoadAndDelete(id)
	if !ok {
		return fmt.Errorf("watcher with ID %s not found", id)
	}

	err := watcher.(*Watcher).close()
	if err != nil {
		return err
	}

	err = w.taskManager.RemoveJob(id)
	if err != nil {
		return fmt.Errorf("error removing watcher: %w", err)
	}

	return nil
}

// Responses returns a channel that will receive all watchers' responses. The channel will
// be closed when the Wadjit is closed. Note: Unless sends on the response channel are
// consumed, a block may occur.
func (w *Wadjit) Responses() <-chan WatcherResponse {
	return w.respExportChan
}

// WatcherIDs returns a slice of strings containing the IDs of all active watchers.
func (w *Wadjit) WatcherIDs() []string {
	var ids []string
	w.watchers.Range(func(key, _ any) bool {
		ids = append(ids, key.(string))
		return true
	})
	return ids
}

// listenForResponses consumes the channel where Watchers send responses from their monitoring jobs
// and forwards those responses to the externally facing channel.
func (w *Wadjit) listenForResponses() {
	for {
		select {
		case resp, ok := <-w.respGatherChan:
			if !ok {
				return // Channel closed
			}
			if w.ctx.Err() != nil {
				return // Context canceled
			}

			// Send the response to the external facing channel
			// TODO: consider adding Watcher response metrics here
			w.respExportChan <- resp
		case <-w.ctx.Done():
			return
		}
	}
}

// New creates and returns a new Wadjit. Optional functional options can be supplied to tune the
// internal task manager and other configuration. Note: Unless sends on the response channel are
// consumed, a block may occur.
func New(opts ...Option) *Wadjit {
	ctx, cancel := context.WithCancel(context.Background())

	// Apply and validate options
	options := applyOptions(opts)

	// Create task manager and Wadjit
	tm := taskman.New(options.taskmanOptions...)
	w := &Wadjit{
		ctx:    ctx,
		cancel: cancel,
		// TODO: set logger
		taskManager:         tm,
		respGatherChan:      make(chan WatcherResponse, options.bufferSize),
		respExportChan:      make(chan WatcherResponse, options.bufferSize),
		defaultDNSPolicy:    options.defaultDNSPolicy,
		hasDefaultDNSPolicy: options.hasDefaultDNSPolicy,
	}

	w.closeWG.Add(1)
	go func() {
		w.listenForResponses()
		w.closeWG.Done()
	}()

	return w
}

// Option configures the behavior of a Wadjit instance created by New.
type Option func(*options)

// options holds configuration for creating a Wadjit instance, including options for the internal
// task manager.
type options struct {
	bufferSize          int
	defaultDNSPolicy    DNSPolicy
	hasDefaultDNSPolicy bool
	logger              zerolog.Logger
	loggerSet           bool

	taskmanOptions []taskman.TMOption
}

// WithBufferSize configures the buffer size for response channels. A negative value will be
// ignored.
func WithBufferSize(size int) Option {
	return func(o *options) {
		if size < 0 {
			return
		}
		o.bufferSize = size
	}
}

// WithDefaultDNSPolicy sets a default DNS policy for HTTP endpoints created without an explicit
// policy. Endpoints configured via WithDNSPolicy override this default.
func WithDefaultDNSPolicy(policy DNSPolicy) Option {
	return func(o *options) {
		o.defaultDNSPolicy = policy
		o.hasDefaultDNSPolicy = true
	}
}

// WithLogger sets the Wadjit and the internal task manager's loggers.
func WithLogger(logger zerolog.Logger) Option {
	return func(o *options) {
		o.logger = logger
		o.loggerSet = true

		// Clamp taskman log level to at least INFO
		logLevel := max(logger.GetLevel(), zerolog.InfoLevel)
		log := logger.Level(logLevel)
		o.taskmanOptions = append(o.taskmanOptions, taskman.WithLogger(log))
	}
}

// WithTaskmanOptions forwards the provided task manager options to the internal task manager.
func WithTaskmanOptions(opts ...taskman.TMOption) Option {
	return func(o *options) {
		if len(opts) == 0 {
			return
		}
		var filtered []taskman.TMOption
		for _, opt := range opts {
			if opt == nil {
				continue
			}
			filtered = append(filtered, opt)
		}
		if len(filtered) == 0 {
			return
		}
		owned := append([]taskman.TMOption(nil), filtered...)
		o.taskmanOptions = append(o.taskmanOptions, owned...)
	}
}

// WithTaskmanMode configures the internal task manager execution mode.
func WithTaskmanMode(mode taskman.ExecMode) Option {
	return WithTaskmanOptions(taskman.WithMode(mode))
}

// applyOptions applies the provided Option slice to the internal options struct. Defaults are
// applied here, and if an option is nil, it is ignored.
func applyOptions(opts []Option) options {
	cfg := options{
		bufferSize: defaultResponseChanBufferSize,
	}

	for _, opt := range opts {
		if opt == nil {
			continue
		}
		opt(&cfg)
	}

	// Ensure sane values and defaults
	if cfg.bufferSize < 0 {
		cfg.bufferSize = defaultResponseChanBufferSize
	}
	if !cfg.loggerSet {
		cfg.logger = zerolog.Nop()
	}

	return cfg
}
