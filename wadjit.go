// Package wadjit provides a framework for monitoring HTTP and WebSocket endpoints.
package wadjit

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"sync"
	"time"

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
	metricsSink    MetricsSink

	defaultDNSPolicy            DNSPolicy
	hasDefaultDNSPolicy         bool
	defaultHTTPTimeouts         HTTPTimeouts
	hasDefaultHTTPTimeouts      bool
	defaultWSTimeouts           WSTimeouts
	hasDefaultWSTimeouts        bool
	defaultMaxResponseBytes     int64
	hasDefaultMaxResponseBytes  bool
	defaultWSMaxMessageBytes    int64
	hasDefaultWSMaxMessageBytes bool
	watcherJitter               time.Duration
	metricsSampleRate           float64

	ctx    context.Context
	cancel context.CancelFunc

	closeErr  error
	closeOnce sync.Once // Ensures idempotency of the Close method
	closeWG   sync.WaitGroup
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
		taskManager:                 tm,
		respGatherChan:              make(chan WatcherResponse, options.bufferSize),
		respExportChan:              make(chan WatcherResponse, options.bufferSize),
		metricsSink:                 options.metricsSink,
		metricsSampleRate:           options.metricsSampleRate,
		defaultDNSPolicy:            options.defaultDNSPolicy,
		hasDefaultDNSPolicy:         options.hasDefaultDNSPolicy,
		defaultHTTPTimeouts:         options.defaultHTTPTimeouts,
		hasDefaultHTTPTimeouts:      options.hasDefaultHTTPTimeouts,
		defaultWSTimeouts:           options.defaultWSTimeouts,
		hasDefaultWSTimeouts:        options.hasDefaultWSTimeouts,
		defaultMaxResponseBytes:     options.defaultMaxResponseBytes,
		hasDefaultMaxResponseBytes:  options.hasDefaultMaxResponseBytes,
		defaultWSMaxMessageBytes:    options.defaultWSMaxMessageBytes,
		hasDefaultWSMaxMessageBytes: options.hasDefaultWSMaxMessageBytes,
		watcherJitter:               options.watcherJitter,
	}

	w.closeWG.Add(1)
	go func() {
		w.listenForResponses()
		w.closeWG.Done()
	}()

	return w
}

// applyDefaultDNSPolicy applies the default DNS policy to the watcher if it is not already set.
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

// applyDefaultHTTPTimeouts applies the default HTTP timeouts to the watcher if not already set.
func (w *Wadjit) applyDefaultHTTPTimeouts(watcher *Watcher) {
	if !w.hasDefaultHTTPTimeouts || watcher == nil {
		return
	}

	for i := range watcher.Tasks {
		if endpoint, ok := watcher.Tasks[i].(*HTTPEndpoint); ok && endpoint != nil {
			if endpoint.timeoutsSet {
				continue
			}
			endpoint.timeouts = w.defaultHTTPTimeouts
			endpoint.timeoutsSet = true
		}
	}
}

// applyDefaultWSTimeouts applies the default WebSocket timeouts to the watcher if not already set.
func (w *Wadjit) applyDefaultWSTimeouts(watcher *Watcher) {
	if !w.hasDefaultWSTimeouts || watcher == nil {
		return
	}

	for i := range watcher.Tasks {
		if endpoint, ok := watcher.Tasks[i].(*WSEndpoint); ok && endpoint != nil {
			if endpoint.timeoutsSet {
				continue
			}
			endpoint.timeouts = w.defaultWSTimeouts
			endpoint.timeoutsSet = true
		}
	}
}

// applyDefaultMaxResponseBytes applies the default max response bytes to HTTP endpoints if not set.
func (w *Wadjit) applyDefaultMaxResponseBytes(watcher *Watcher) {
	if !w.hasDefaultMaxResponseBytes || watcher == nil {
		return
	}

	for i := range watcher.Tasks {
		if endpoint, ok := watcher.Tasks[i].(*HTTPEndpoint); ok && endpoint != nil {
			if endpoint.maxResponseBytesSet {
				continue
			}
			endpoint.maxResponseBytes = w.defaultMaxResponseBytes
			endpoint.maxResponseBytesSet = true
		}
	}
}

// applyDefaultWSMaxMessageBytes applies the default max message bytes to WS endpoints if not set.
func (w *Wadjit) applyDefaultWSMaxMessageBytes(watcher *Watcher) {
	if !w.hasDefaultWSMaxMessageBytes || watcher == nil {
		return
	}

	for i := range watcher.Tasks {
		if endpoint, ok := watcher.Tasks[i].(*WSEndpoint); ok && endpoint != nil {
			if endpoint.maxMessageBytesSet {
				continue
			}
			endpoint.maxMessageBytes = w.defaultWSMaxMessageBytes
			endpoint.maxMessageBytesSet = true
		}
	}
}

// applyJitter applies the configured jitter to the watcher.
func (w *Wadjit) applyJitter(watcher *Watcher) {
	if watcher == nil || w.watcherJitter <= 0 {
		return
	}
	watcher.jitter = w.watcherJitter
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

			// Prepare response metadata for metrics before emitting.
			resp.prepareForMetrics()

			w.observeResponse(resp)

			// Send the response to the external facing channel
			// TODO: consider adding Watcher response metrics here
			w.respExportChan <- resp
		case <-w.ctx.Done():
			return
		}
	}
}

// AddWatcher adds a Watcher to the Wadjit, starting it in the process.
func (w *Wadjit) AddWatcher(watcher *Watcher) error {
	w.applyDefaultDNSPolicy(watcher)
	w.applyDefaultHTTPTimeouts(watcher)
	w.applyDefaultWSTimeouts(watcher)
	w.applyDefaultMaxResponseBytes(watcher)
	w.applyDefaultWSMaxMessageBytes(watcher)
	w.applyJitter(watcher)

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

	w.observeEvent("watcher_added", map[string]any{
		"watcher_id": watcher.ID,
		"tasks":      len(watcher.Tasks),
		"cadence":    watcher.Cadence,
	})

	return nil
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

// PauseWatcher pauses a watcher's scheduled execution. The watcher remains registered but will
// not execute tasks until ResumeWatcher is called. Returns an error if the watcher ID does not
// exist or if the underlying task manager fails to pause the job.
func (w *Wadjit) PauseWatcher(id string) error {
	if err := w.taskManager.PauseJob(id); err != nil {
		return err
	}
	w.observeEvent("watcher_paused", map[string]any{"watcher_id": id})
	return nil
}

// ResumeWatcher resumes a previously paused watcher's scheduled execution. Returns an error if
// the watcher ID does not exist or if the underlying task manager fails to resume the job.
func (w *Wadjit) ResumeWatcher(id string) error {
	if err := w.taskManager.ResumeJob(id); err != nil {
		return err
	}
	w.observeEvent("watcher_resumed", map[string]any{"watcher_id": id})
	return nil
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

	w.observeEvent("watcher_removed", map[string]any{"watcher_id": id})

	return nil
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

// Metrics returns metrics from the Wadjit's internal task manager.
func (w *Wadjit) Metrics() taskman.TaskManagerMetrics {
	return w.taskManager.Metrics()
}

// observeResponse reports a response to the metrics sink, if configured.
// The sink is invoked asynchronously to avoid blocking response propagation.
func (w *Wadjit) observeResponse(resp WatcherResponse) {
	if w.metricsSink == nil {
		return
	}
	if !w.shouldSample(resp) {
		return
	}
	go func(r WatcherResponse) {
		defer func() { _ = recover() }()
		w.metricsSink.ObserveResponse(r.MetricsView())
	}(resp)
}

// observeEvent reports an internal event to the metrics sink, if configured.
func (w *Wadjit) observeEvent(name string, fields map[string]any) {
	if w.metricsSink == nil {
		return
	}
	go func() {
		defer func() { _ = recover() }()
		w.metricsSink.ObserveEvent(name, fields)
	}()
}

// shouldSample determines whether a response should be forwarded to the metrics sink.
// Sampling is deterministic per (WatcherID, TaskID) pair to keep time series stable.
func (w *Wadjit) shouldSample(resp WatcherResponse) bool {
	if w.metricsSampleRate >= 1 {
		return true
	}
	if w.metricsSampleRate <= 0 {
		return false
	}

	h := fnv.New64a()
	_, _ = h.Write([]byte(resp.WatcherID))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(resp.TaskID))
	v := h.Sum64()
	const denom = float64(^uint64(0))
	return float64(v) <= w.metricsSampleRate*denom
}

// Option configures the behavior of a Wadjit instance created by New.
type Option func(*options)

// options holds configuration for creating a Wadjit instance, including options for the internal
// task manager.
type options struct {
	bufferSize                  int
	defaultDNSPolicy            DNSPolicy
	hasDefaultDNSPolicy         bool
	defaultHTTPTimeouts         HTTPTimeouts
	hasDefaultHTTPTimeouts      bool
	defaultWSTimeouts           WSTimeouts
	hasDefaultWSTimeouts        bool
	defaultMaxResponseBytes     int64
	hasDefaultMaxResponseBytes  bool
	defaultWSMaxMessageBytes    int64
	hasDefaultWSMaxMessageBytes bool
	watcherJitter               time.Duration
	logger                      zerolog.Logger
	loggerSet                   bool
	taskmanLogLevel             *zerolog.Level
	metricsSink                 MetricsSink
	metricsSampleRate           float64

	taskmanOptions []taskman.Option
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

// WithDefaultHTTPTimeouts sets default HTTP timeout values for HTTP endpoints created without
// explicit timeout configuration. Endpoints configured via WithHTTPTimeouts override this default.
func WithDefaultHTTPTimeouts(timeouts HTTPTimeouts) Option {
	return func(o *options) {
		o.defaultHTTPTimeouts = timeouts
		o.hasDefaultHTTPTimeouts = true
	}
}

// WithDefaultWSTimeouts sets default WebSocket timeout values for WebSocket endpoints created
// without explicit timeout configuration. Endpoints configured via WithWSTimeouts override this
// default.
func WithDefaultWSTimeouts(timeouts WSTimeouts) Option {
	return func(o *options) {
		o.defaultWSTimeouts = timeouts
		o.hasDefaultWSTimeouts = true
	}
}

// WithDefaultMaxResponseBytes sets the default maximum HTTP response body size for all HTTP
// endpoints. Endpoints configured via WithMaxResponseBytes override this default. A value of 0
// or negative means no limit.
func WithDefaultMaxResponseBytes(n int64) Option {
	return func(o *options) {
		o.defaultMaxResponseBytes = n
		o.hasDefaultMaxResponseBytes = true
	}
}

// WithDefaultWSMaxMessageBytes sets the default maximum WebSocket message size for all WebSocket
// endpoints. Endpoints configured via WithWSMaxMessageBytes override this default. A value of 0
// or negative means no limit (uses gorilla's default).
func WithDefaultWSMaxMessageBytes(n int64) Option {
	return func(o *options) {
		o.defaultWSMaxMessageBytes = n
		o.hasDefaultWSMaxMessageBytes = true
	}
}

// WithWatcherJitter sets the maximum random offset to apply to watcher initial execution times.
// The jitter is applied once in the range [-jitter, +jitter] relative to the scheduled cadence time.
// This does not cause drift - subsequent executions maintain exact cadence intervals after the
// jittered first execution. This helps avoid thundering herds when multiple watchers start
// simultaneously. Negative values are clamped to zero (no jitter).
func WithWatcherJitter(jitter time.Duration) Option {
	return func(o *options) {
		if jitter < 0 {
			jitter = 0
		}
		o.watcherJitter = jitter
	}
}

// WithLogger sets the Wadjit and the internal task manager's loggers.
func WithLogger(logger zerolog.Logger) Option {
	return func(o *options) {
		o.logger = logger
		o.loggerSet = true

		// Clamp taskman log level to at least INFO
		logLevel := max(logger.GetLevel(), zerolog.DebugLevel)
		log := logger.Level(logLevel)
		o.taskmanOptions = append(o.taskmanOptions, taskman.WithLogger(log))
	}
}

// WithTaskmanLogLevel sets the log level for the internal task manager independently from the
// Wadjit logger. This allows fine-grained control over taskman's logging verbosity. If both
// WithLogger and WithTaskmanLogLevel are used, the taskman log level takes precedence for the
// task manager.
func WithTaskmanLogLevel(level zerolog.Level) Option {
	return func(o *options) {
		o.taskmanLogLevel = &level
	}
}

// WithTaskmanOptions forwards the provided task manager options to the internal task manager.
func WithTaskmanOptions(opts ...taskman.Option) Option {
	return func(o *options) {
		if len(opts) == 0 {
			return
		}
		var filtered []taskman.Option
		for _, opt := range opts {
			if opt == nil {
				continue
			}
			filtered = append(filtered, opt)
		}
		if len(filtered) == 0 {
			return
		}
		owned := append([]taskman.Option(nil), filtered...)
		o.taskmanOptions = append(o.taskmanOptions, owned...)
	}
}

// WithMetricsSink configures a sink to observe responses and internal events. The sink is invoked
// best-effort and must be non-blocking.
func WithMetricsSink(ms MetricsSink) Option {
	return func(o *options) {
		o.metricsSink = ms
	}
}

// WithMetricsSampleRate configures the fraction of responses forwarded to the metrics sink.
// Values are clamped to [0,1]. Defaults to 1.0 (no sampling).
func WithMetricsSampleRate(rate float64) Option {
	return func(o *options) {
		switch {
		case rate < 0:
			o.metricsSampleRate = 0
		case rate > 1:
			o.metricsSampleRate = 1
		default:
			o.metricsSampleRate = rate
		}
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
		bufferSize:        defaultResponseChanBufferSize,
		metricsSampleRate: 1.0,
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

	// If a specific taskman log level is set, apply it as the final taskman logger option.
	// This ensures it overrides any logger set by WithLogger.
	if cfg.taskmanLogLevel != nil {
		// Use the Wadjit logger as the base, but set it to the specified level
		taskmanLogger := cfg.logger.Level(*cfg.taskmanLogLevel)
		cfg.taskmanOptions = append(cfg.taskmanOptions, taskman.WithLogger(taskmanLogger))
	}

	return cfg
}
