# todo

## v0.2.0

- Add a second WS task implementation, for when responses can't be correlated with requests
- Add timings to requests and WS sends, to be able to track latency of requests and WS messages
- Improve readme

## various ideas

- Create a new type of `Watcher`, to watch WS subscriptions
  - WS subscriptions do not fit into the current `Watcher` model, which is based on polling
- Add metrics for stats like `Watcher` timing accuracy, and `Watcher` error rates
- Add configuration options, e.g. for buffer sizes, timeouts, metrics on/off etc.
- WS reconnect strategy evaluation
  - Change for an active reconnect, with exponential backoff, would also include keep-alive measures
  - Introduce a pre-execute check that the connection is alive and ready to be used
- Make WS use read and write deadlines to avoid blocking
