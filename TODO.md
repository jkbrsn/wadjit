# todo

## v0.6.0

- Add `Watcher` metrics (per watcher and/or overall)
  - Timing accuracy/skew
  - Error/success rates
  - Response times (?)
  - Backlog size
  - Channel pressure
- Improve metadata carry in tasks/responses

## v0.7.0

- (maybe) Support for concurrent add/remove of watchers
- Expose opt-in configuration via functional options, some option ideas:
  - Buffer sizes
  - Metric sink
  - Deadline times
  - Read upon response received, to avoid adding lag to the timing of DataTransferTime or RequestTimeTotal
- Improve readme; add more (real) implementation examples, mention WS support, show response handling more

## various ideas

- Create a new type of `Watcher`, to watch WS subscriptions
  - WS subscriptions do not fit into the current `Watcher` model, which is based on polling
- Add configuration options, e.g. for buffer sizes, timeouts, metrics on/off etc.
- WS reconnect strategy evaluation
  - Change for an active reconnect, with exponential backoff, would also include keep-alive measures
  - Introduce a pre-execute check that the connection is alive and ready to be used
- Make WS use read and write deadlines to avoid blocking
