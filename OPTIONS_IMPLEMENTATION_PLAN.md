# Wadjit Options Implementation Plan (v0.10.x)

This plan covers the five prioritized options. Each is a self-contained phase with goal vision, purpose/value, approach, and code sketch. After these, a short list of future potentials is provided.

---

## Phase 1 — Metrics Sink

- **Goal / Vision**: Allow users to plug in a lightweight sink to observe watcher responses and internal events without blocking delivery.
- **Purpose & Value**: Enables metrics export (Prometheus, StatsD, OTEL) without forcing a specific backend; keeps latency low by using non-blocking emission.
- **Approach**:
  - Define a `MetricsSink` interface (e.g., `ObserveResponse(resp WatcherResponse)` and `ObserveInternal(event string, fields map[string]any)` to start minimal).
  - Add manager option `WithMetricsSink(sink MetricsSink)` storing it in `options` and wiring to `Wadjit`.
  - In `listenForResponses` (before enqueue to `respExportChan`), invoke sink in a goroutine or best-effort non-blocking call to avoid backpressure.
  - Optionally emit basic internal events (e.g., watcher add/remove, errors) from `AddWatcher/RemoveWatcher`.
- **Code Sketch**:
  - `type MetricsSink interface { ObserveResponse(WatcherResponse); ObserveEvent(name string, fields map[string]any) }`
  - `func WithMetricsSink(ms MetricsSink) Option { return func(o *options) { o.metricsSink = ms } }`
  - In `listenForResponses`: `if w.metricsSink != nil { go w.metricsSink.ObserveResponse(resp) }`

## Phase 2 — Deadline / Timeout Controls

- **Goal / Vision**: Make HTTP/WS timeouts configurable per endpoint to bound latency and stalled connections.
- **Purpose & Value**: Protects callers from hung dials/reads; aligns Wadjit with SLOs and varied endpoint behaviors.
- **Approach**:
  - HTTP: add option `WithHTTPTimeouts(total, header, idle, tls time.Duration)` (or a struct). Apply inside `HTTPEndpoint.Initialize` by modifying the cloned `http.Transport` (`ResponseHeaderTimeout`, `IdleConnTimeout`, `TLSHandshakeTimeout`) and setting `client.Timeout` for total deadline.
  - WS: add `WithWSTimeouts(handshake, read, write, pingInterval time.Duration)`; use a custom `websocket.Dialer` with `HandshakeTimeout`, and set per-connection read/write deadlines in `wsOneHit` and `wsPersistent` (write path + readPump loop) with `SetReadDeadline/SetWriteDeadline`. Optionally schedule pings on persistent mode using `pingInterval`.
  - Expose defaults that mirror Go stdlib where unspecified.
- **Code Sketch**:
  - Extend `HTTPEndpoint` with `timeouts httpTimeouts` and corresponding option that patches transport/client fields.
  - Extend `WSEndpoint` with `wsTimeouts` applied in dial/connect and before reads/writes: `conn.SetReadDeadline(time.Now().Add(wsTimeouts.read))`.

## Phase 3 — Taskman Log Level Override

- **Goal / Vision**: Decouple taskman logging verbosity from Wadjit’s logger level.
- **Purpose & Value**: Lets users keep Wadjit quiet while turning up scheduler diagnostics, or vice versa.
- **Approach**:
  - Add option `WithTaskmanLogLevel(level zerolog.Level)` or `WithTaskmanLogger(logger zerolog.Logger)`.
  - Store in `options`; when applying options, append a `taskman.WithLogger(customLogger)` that uses the provided level irrespective of `WithLogger`.
  - Preserve existing `WithLogger` behavior; if both are set, taskman-specific wins for taskman.
- **Code Sketch**:
  - `func WithTaskmanLogLevel(l zerolog.Level) Option { return func(o *options) { o.taskmanLogLevel = &l } }`
  - In `applyOptions`, after `WithLogger`, inject the dedicated taskman logger when the level is provided.

## Phase 4 — Cadence Jitter

- **Goal / Vision**: Stagger watcher executions to avoid thundering herds when many share the same cadence.
- **Purpose & Value**: Smooths load on targets and on Wadjit’s scheduler; reduces correlated spikes.
- **Approach**:
  - Add option `WithWatcherJitter(duration time.Duration)` (absolute) or percentage variant; store on `Wadjit`.
  - In `Watcher.job()`, when computing `NextExec`, add a random offset in `[-jitter, +jitter]` (clamped to non-negative cadence). Use `crypto/rand` or `math/rand` seeded once.
  - Consider applying jitter per scheduling cycle (recompute NextExec on each reschedule) or only on initial schedule; document choice (recommend per-cycle for ongoing smoothing).
- **Code Sketch**:
  - `offset := time.Duration(rand.Int63n(2*int64(jitter)+1)) - jitter; job.NextExec = time.Now().Add(w.Cadence + offset)`.

## Phase 5 — Response Size Limits

- **Goal / Vision**: Allow users to cap payload size per task to protect memory and surface truncation explicitly.
- **Purpose & Value**: Prevents unbounded memory use on large responses or runaway WS messages; provides deterministic failure/truncation semantics.
- **Approach**:
  - HTTP: add `WithMaxResponseBytes(n int64)` to `HTTPEndpoint`; wrap `resp.Body` with `io.LimitReader` (n+1 to detect overflow). If overflow, close/destroy body and return a sentinel error or metadata flag (e.g., `Metadata().Size` + `Metadata().Headers.Add("X-Wadjit-Truncated", "true")`).
  - WS: add `WithWSMaxMessageBytes(n int64)`; set `conn.SetReadLimit(n)` on each connection (one-hit and persistent). If limit exceeded, surface the read error via `WatcherResponse.Err` so callers see truncation.
  - Expose a shared field in `TaskResponseMetadata` (e.g., `Truncated bool`) to signal capped results.
- **Code Sketch**:
  - HTTP: `reader := io.LimitReader(resp.Body, max+1); data, err := io.ReadAll(reader); if int64(len(data)) > max { err = errMaxSize }`
  - WS: `conn.SetReadLimit(maxBytes)` right after dialing.

---

### Future Potential (not in current scope)
- `WithResponseBuffers` to split internal/export channel sizes.
- `WithDefaultHTTPReadFast` with size guard.
- Additional DNS guard-rail actions (cool-downs, per-IP eviction).
