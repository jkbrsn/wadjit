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

### Task Types

- `HTTPEndpoint`: For making HTTP/HTTPS requests
- `WSEndpoint`: For WebSocket connections (one-shot and persistent JSON-RPC)

## Contributing

Thank you for considering to contribute to this project. For contributions, please open a GitHub issue with your questions and suggestions. Before submitting an issue, have a look at the existing [TODO list](TODO.md) to see if your idea is already in the works.
