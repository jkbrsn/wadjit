# todo

## v0.10.x

- Add `Watcher` metrics (per watcher and/or overall)
  - Timing accuracy/skew
  - Error/success rates
  - Response times (?)
  - Backlog size
  - Channel pressure
- (maybe) Support for concurrent add/remove of watchers
  - Might be better to just not support it and let it be clear the management of watchers is left to the user
- Expose further options:
  - Buffer sizes
  - Metric sink
  - Deadline times
  - Read upon response received, to avoid adding lag to the timing of DataTransferTime or RequestTimeTotal
  - `taskman` specific log level
- Add more metadata to `WatcherResponse`:
  - Specify protocol (HTTP/WS)
  - Specify error type (e.g. connection error, response error)
- Implement DNS policy usage for WS endpoints

## Various

- Improve metadata carry in tasks/responses (`Watcher.ID` only current metadata)
- Create a new type of `Watcher`, to watch WS subscriptions
  - WS subscriptions do not fit into the current `Watcher` model, which is based on polling
- Add configuration options, e.g. for buffer sizes, timeouts, metrics on/off etc.
- WS reconnect strategy evaluation
  - Change for an active reconnect, with exponential backoff, would also include keep-alive measures
  - Introduce a pre-execute check that the connection is alive and ready to be used
- Make WS use read and write deadlines to avoid blocking

### Extend DNSGuardRailPolicy patterns

Ideas on additional guard-rail patterns that could be useful, especially for long-lived monitoring tasks:

- Status-Code Buckets
  - Today any ≥500 response increments the counter. You might expose separate thresholds/actions for different classes (e.g. 429 vs 500/502 vs TLS handshake failures). That lets you force a reconnect faster for upstream failures while staying tolerant of transient rate limits.
- Latency/Timeout Spike Guard
  - Track rolling RTT or timeouts; if the connect or TLS handshake time balloons, force a lookup/reconnect even if responses are still 200. Handy when DNS is stale and traffic drifts across regions.
- Lookup Error Ceiling
  - Right now guard rails only trigger after request failures. Consider a mode that watches consecutive DNS lookup failures (e.g. NXDOMAIN, SERVFAIL). After N misses, you could fall back to static overrides, widen TTLMin, or pause the watcher with a surfaced event.
- Circuit-Breaker Holdoff
  - Add an action that puts the endpoint into a cool-down after the threshold (stop scheduling new requests for X seconds). For monitoring pipelines this avoids hammering a known-bad target while keeping visibility.
- Multi-Guard Priorities
  - Allow stacking guards with different thresholds/actions: e.g., first guard flushes connections after 3 errors, second triggers a forced lookup after 5, third escalates to notifying observers or switching to a backup endpoint after 10.
- Success Reset Policies
  - Currently any success clears the counter. You could optionally require M consecutive successes before fully resetting to avoid oscillation in flappy environments.
- Per-Address Failure Tracking
  - When DNS returns multiple IPs, track failures per address; if only one backend is sick, evict that IP from the rotation while keeping others, rather than forcing all lookups.
