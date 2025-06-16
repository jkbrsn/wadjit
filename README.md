# wadjit [![Go Documentation](http://img.shields.io/badge/go-documentation-blue.svg?style=flat-square)][godocs]

[godocs]: http://godoc.org/github.com/jkbrsn/go-wadjit

Wadjit (pronounced /ˈwɒdʒɪt/, or "watch it") is a program for endpoint monitoring and analysis.

> Wadjet is the ancient Egyptian goddess of protection and royal authority. She is sometimes shown as the Eye of Ra, acting as a protector of the country and the king, and her vigilant eye would watch over the land. - [Wikipedia](https://en.wikipedia.org/wiki/Wadjet)

`wadjit.New()` creates a manager for an arbitrary number of watchers. The watchers monitor pre-defined endpoints according to their configuration, and feed the results back to the manager. A single Wadjit manager can hold watchers for many different tasks, as responses on the response channel are separated by watcher ID, or you may choose to create several managers as a more strict separation of concerns.

## Installation

```bash
go get -u github.com/jkbrsn/go-wadjit@latest
```

## Usage

In pseduocode, these steps are what's needed to make use of the Wadjit watcher manager:

```go
// Initialize manager
manager := wadjit.New()
defer manager.Close()

// Add watcher with name, cadence, and tasks
myWatcher := wadjit.NewWatcher(
    "My watcher",
    5*time.Second,
    someTasks(),
)
manager.AddWatcher(myWatcher)

// Start consuming responses, unless we do this the jobs will block when the channel is full
respChannel := manager.Responses()
for {
    resp, ok := <-respChannel
    // Handle resp data
}
```

For a detailed example of how to use this package, have a look at [_example/main.go](./_example/main.go) and try running it with:

```bash
go run _example/main.go
```

## API Reference

### Wadjit

- `New() *Wadjit`: Creates a new Wadjit instance
- `AddWatcher(watcher *Watcher) error`: Adds a watcher to the manager
- `AddWatchers(watchers ...*Watcher) error`: Adds multiple watchers at once
- `RemoveWatcher(id string) error`: Removes a watcher by ID
- `Responses() <-chan WatcherResponse`: Returns a channel for receiving responses
- `Metrics() TaskManagerMetrics`: Returns metrics about the task manager
- `Close() error`: Stops all watchers and cleans up resources

### Watcher

- `NewWatcher(id string, cadence time.Duration, tasks []WatcherTask) (*Watcher, error)`: Creates a new watcher
- `Validate() error`: Validates the watcher configuration

### Task Types

- `HTTPEndpoint`: For making HTTP/HTTPS requests
- `WSEndpoint`: For WebSocket connections (both one-time and persistent)

## Contributing

Thank you for considering to contribute to this project. For contributions, please open a GitHub issue with your questions and suggestions. Before submitting an issue, have a look at the existing [TODO list](TODO.md) to see if your idea is already in the works.
