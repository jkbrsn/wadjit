package wadjit

// MetricsSink is a pluggable observer for responses and internal events.
// Implementations must be non-blocking or very fast; Wadjit invokes the sink
// best-effort and does not wait for completion.
type MetricsSink interface {
	ObserveResponse(ResponseMetrics)
	ObserveEvent(name string, fields map[string]any)
}

// ResponseMetrics is a payload-free snapshot of a watcher response suitable for metrics export.
// It intentionally excludes bodies to avoid leaking mutable or large data.
type ResponseMetrics struct {
	WatcherID  string
	TaskID     string
	Target     string
	StatusCode int
	SizeBytes  int64
	Err        string
	Times      RequestTimes
}
