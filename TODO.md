# todo

## v0.7.1

- Add `Watcher` metrics (per watcher and/or overall)
  - Timing accuracy/skew
  - Error/success rates
  - Response times (?)
  - Backlog size
  - Channel pressure
- (maybe) Support for concurrent add/remove of watchers
- Expose opt-in configuration via functional options, some option ideas:
  - Buffer sizes
  - Metric sink
  - Deadline times
  - Read upon response received, to avoid adding lag to the timing of DataTransferTime or RequestTimeTotal
- Improve readme; add more (real) implementation examples, mention WS support, show response handling more
- Clean up linter errors

## various ideas

- Improve metadata carry in tasks/responses (`Watcher.ID` only current metadata)
- Create a new type of `Watcher`, to watch WS subscriptions
  - WS subscriptions do not fit into the current `Watcher` model, which is based on polling
- Add configuration options, e.g. for buffer sizes, timeouts, metrics on/off etc.
- WS reconnect strategy evaluation
  - Change for an active reconnect, with exponential backoff, would also include keep-alive measures
  - Introduce a pre-execute check that the connection is alive and ready to be used
- Make WS use read and write deadlines to avoid blocking
