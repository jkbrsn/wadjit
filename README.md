# wadjit

Wadjit (pronounced /ˈwɒdʒɪt/, or "watch it") is a program for endpoint monitoring and analysis.

> Wadjet is the ancient Egyptian goddess of protection and royal authority. She is sometimes shown as the Eye of Ra, acting as a protector of the country and the king, and her vigilant eye would watch over the land. - [Wikipedia](https://en.wikipedia.org/wiki/Wadjet)

`wadjit.New()` creates a manager for an arbitrary number of watchers. The watchers monitor pre-defined endpoints according to their configuration, and feed the results back to the manager. A single Wadjit manager can hold watchers for many different tasks, as responses on the response channel are separated by watcher ID, or you may choose to create several managers as a more strict separation of concerns.

## Installation

```bash
go get -u github.com/jakobilobi/wadjit@latest
```

## Usage

In pseduocode, these steps are what's needed to make use of the Wadjit watcher manager:

```go
// Initialize manager
manager := wadjit.New()
defer manager.Close()

// Add watcher with name, cadence, and tasks
myWatcher, err := wadjit.NewWatcher(
    "My watcher",
    5*time.Second,
    someTasks(),
)
err = manager.AddWatcher(myWatcher)

// Start manager and consume responses
respChannel := manager.Start()
for {
    resp, ok := <-respChannel
    // Handle resp data
}
```

For a detailed example of how to use this package, have a look at [_example/main.go](./_example/main.go) and try running it with:

```bash
go run _example/main.go
```

## Contributing

Thank you for considering to contribute to this project. For contributions, please open a GitHub issue with your questions and suggestions. Before submitting an issue, have a look at the existing [TODO list](TODO.md) to see if your idea is already in the works.
