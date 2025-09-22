# wadjit [![Go Reference](https://pkg.go.dev/badge/github.com/jkbrsn/wadjit.svg)](https://pkg.go.dev/github.com/jkbrsn/wadjit)

Wadjit (pronounced /ˈwɒdʒɪt/, or "watch it") is a program for endpoint monitoring and analysis.

> Wadjet is the ancient Egyptian goddess of protection and royal authority. She is sometimes shown as the Eye of Ra, acting as a protector of the country and the king, and her vigilant eye would watch over the land. - [Wikipedia](https://en.wikipedia.org/wiki/Wadjet)

`wadjit.New()` creates a manager for an arbitrary number of watchers. The watchers monitor pre-defined endpoints according to their configuration, and feed the results back to the manager. A single Wadjit manager can hold watchers for many different tasks, as responses on the response channel are separated by watcher ID, or you may choose to create several managers as a more strict separation of concerns.

## Installation

```bash
go get github.com/jkbrsn/wadjit@latest
```

## Features

- HTTP + WS: Monitor HTTP and WebSocket endpoints, with or without TLS.
- WS modes: One-shot messages and persistent connections for JSON-RPC.
- Batched watchers: Schedule many tasks per watcher at a fixed cadence.
- Buffered responses: Non-blocking channel with watcher IDs and metadata.
- Metrics: Access scheduler metrics via `Metrics()`.

## Quick Start

Minimal example with one HTTP and one WS task and basic response handling:

```go
package main

import (
    "fmt"
    "net/http"
    "net/url"
    "time"

    "github.com/jkbrsn/wadjit"
)

func main() {
    // Initialize manager (options available)
    manager := wadjit.New()
    defer manager.Close()

    // Build tasks
    httpTask := &wadjit.HTTPEndpoint{
        Header:  make(http.Header),
        Method:  http.MethodGet,
        URL:     &url.URL{Scheme: "https", Host: "httpbin.org", Path: "/get"},
    }
    wsTask := &wadjit.WSEndpoint{
        Mode:    wadjit.OneHitText,
        Payload: []byte("hello"),
        URL:     &url.URL{Scheme: "wss", Host: "ws.postman-echo.com", Path: "/raw"},
    }

    // Add a watcher with a 5s cadence
    watcher, err := wadjit.NewWatcher("example", 5*time.Second, wadjit.WatcherTasksToSlice(httpTask, wsTask))
    if err == nil {
        _ = manager.AddWatcher(watcher)
    }

    // Consume responses (must be read to avoid backpressure)
    for resp := range manager.Responses() {
        if resp.Err != nil {
            fmt.Printf("%s error: %v\n", resp.WatcherID, resp.Err)
            continue
        }
        body, err := resp.Data()
        if err != nil { continue }
        fmt.Printf("%s %s -> %s\n", resp.WatcherID, resp.URL, string(body))
        // Access timing and header metadata
        md := resp.Metadata()
        fmt.Printf("latency: %v headers: %v\n", md.TimeData.Latency, md.Headers)
    }
}
```

Need to tweak the scheduler? Wrap the constructor call: `wadjit.New(wadjit.WithTaskmanMode(taskman.ModeOnDemand))` or forward `taskman` options via `wadjit.WithTaskmanOptions(...)`.

## Examples

See a fuller runnable example in [`examples/example.go`](examples/example.go) and run it with:

```bash
go run ./examples
```

## Tests and CI

Run tests with:

```bash
make test
```

Other targets in the `Makefile` include `fmt` for formatting the code and `lint` for running the linter.

These targets are also used in the GitHub CI pipeline, see [`.github/workflows/ci.yml`](.github/workflows/ci.yml) for details.

## API Reference

### Wadjit

- `New(opts ...Option) *Wadjit`: Creates a new Wadjit instance; options can tweak the internal task manager (for example `WithTaskmanMode`).
- `AddWatcher(watcher *Watcher) error`: Adds a watcher to the manager
- `AddWatchers(watchers ...*Watcher) error`: Adds multiple watchers at once
- `RemoveWatcher(id string) error`: Removes a watcher by ID
- `Clear() error`: Stops and removes all watchers, keeps manager running
- `WatcherIDs() []string`: Lists IDs of active watchers
- `Responses() <-chan WatcherResponse`: Returns a channel for receiving responses
- `Metrics() taskman.TaskManagerMetrics`: Returns task scheduler metrics
- `Close() error`: Stops all watchers and cleans up resources

### Watcher

- `NewWatcher(id string, cadence time.Duration, tasks []WatcherTask) (*Watcher, error)`: Creates a new watcher
- `Validate() error`: Validates the watcher configuration

### Task: HTTPEndpoint

For making HTTP/HTTPS requests

#### DNS Policy

Wadjit's HTTP task can stay on long-lived keep-alive connections or routinely force new dials depending on the configured [DNS policy](dns_policy.go). Each mode determines when the `dnsPolicyManager` refreshes name resolution and flushes idle connections:

- `DNSRefreshDefault` mirrors Go's standard `http.Transport`: connections stay warm until some other condition closes them.
- `DNSRefreshStatic` bypasses DNS entirely by dialing a fixed `netip.AddrPort`, making every reuse hit the same IP.
- `DNSRefreshSingleLookup` does one resolution during initialization, caches the addresses, and keeps reusing that result indefinitely.
- `DNSRefreshTTL` honors observed DNS TTLs (clamped by optional `TTLMin`/`TTLMax`) and forces a fresh lookup once the TTL elapses. Failed refreshes reuse the cached address by default; set `DisableFallback` to drop it instead. TTLs ≤ 0 are treated as “expire immediately,” ensuring the next request resolves again unless fallback keeps the previous address alive.
- `DNSRefreshCadence` ignores TTL and instead re-lookups on a fixed cadence you supply; it still records the resolver's TTL for observability. Failed refreshes reuse the cached address unless `DisableFallback` is set.

Guard rails add safety nets on top of any mode. Configure `GuardRailPolicy` with a consecutive error threshold, optional rolling window, and an action:

- `GuardRailActionFlush` drops idle connections after the threshold, ensuring the next request redials.
- `GuardRailActionForceLookup` also sets a `forceLookup` flag so the next request performs a fresh DNS resolution before dialing.

##### Examples

Assuming a parsed target URL:

```go
targetURL, _ := url.Parse("https://service.example.com/healthz")
```

- **Default keep-alive**: omit `WithDNSPolicy` to keep Go's stock reuse.

  ```go
  defaultEndpoint := wadjit.NewHTTPEndpoint(targetURL, http.MethodGet)
  ```

- **Static address**: pin the transport to a literal IP:port, bypassing DNS.

  ```go
  staticEndpoint := wadjit.NewHTTPEndpoint(
      targetURL,
      http.MethodGet,
      wadjit.WithDNSPolicy(wadjit.DNSPolicy{
          Mode:       wadjit.DNSRefreshStatic,
          StaticAddr: netip.MustParseAddrPort("198.51.123.123:443"),
      }),
  )
  ```

- **Single lookup**: resolve once when the watcher starts, then reuse indefinitely.

  ```go
  singleLookupEndpoint := wadjit.NewHTTPEndpoint(
      targetURL,
      http.MethodGet,
      wadjit.WithDNSPolicy(wadjit.DNSPolicy{Mode: wadjit.DNSRefreshSingleLookup}),
  )
  ```

- **TTL-aware refresh**: honor dynamic endpoints and force fresh lookups when the TTL expires. Guard rails can force a lookup on repeated failures.

  ```go
  ttlEndpoint := wadjit.NewHTTPEndpoint(
      targetURL,
      http.MethodGet,
      wadjit.WithDNSPolicy(wadjit.DNSPolicy{
          Mode:          wadjit.DNSRefreshTTL,
          TTLMin:        5 * time.Second,
          TTLMax:        30 * time.Second,
          // DisableFallback: true, // opt out of reusing cached addresses when lookups fail
          GuardRail: wadjit.GuardRailPolicy{
              ConsecutiveErrorThreshold: 4,
              Window:                    1 * time.Minute,
              Action:                    wadjit.GuardRailActionForceLookup,
          },
      }),
  )
  ```

- **Fixed cadence refresh**: ignore TTL and re-dial on a clock.

  ```go
  cadenceEndpoint := wadjit.NewHTTPEndpoint(
      targetURL,
      http.MethodGet,
      wadjit.WithDNSPolicy(wadjit.DNSPolicy{
          Mode:    wadjit.DNSRefreshCadence,
          Cadence: 10 * time.Second,
          GuardRail: wadjit.GuardRailPolicy{
              ConsecutiveErrorThreshold: 2,
              Action:                    wadjit.GuardRailActionFlush,
          },
      }),
  )
  ```

The `DNSDecisionCallback` can be used to observe decisions and state transitions.

### Task: WSEndpoint

For WebSocket connections (one-shot and persistent JSON-RPC).

## Contributing

Thank you for considering to contribute to this project. For contributions, please open a GitHub issue with your questions and suggestions. Before submitting an issue, have a look at the existing [TODO list](TODO.md) to see if your idea is already in the works.
