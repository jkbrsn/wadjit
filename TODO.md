# todo

## v0.6.0

- Add metrics for stats like `Watcher` timing accuracy, and `Watcher` error rates
- Improve readme; add more (real) implementation examples, mention WS support, show response handling more
- (maybe) Support for concurrent add/remove of watchers

## various ideas

- Create a new type of `Watcher`, to watch WS subscriptions
  - WS subscriptions do not fit into the current `Watcher` model, which is based on polling
- Add configuration options, e.g. for buffer sizes, timeouts, metrics on/off etc.
- WS reconnect strategy evaluation
  - Change for an active reconnect, with exponential backoff, would also include keep-alive measures
  - Introduce a pre-execute check that the connection is alive and ready to be used
- Make WS use read and write deadlines to avoid blocking
