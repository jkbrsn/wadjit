# todo

## v0.8.0

- Expose functional options:
  - `taskman` mode
  - More...? Or later?
- Support "pause" and "resume" of watchers
  - Dependent on future `taskman` features

## v0.x.x

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

## various ideas

- Improve metadata carry in tasks/responses (`Watcher.ID` only current metadata)
- Create a new type of `Watcher`, to watch WS subscriptions
  - WS subscriptions do not fit into the current `Watcher` model, which is based on polling
- Add configuration options, e.g. for buffer sizes, timeouts, metrics on/off etc.
- WS reconnect strategy evaluation
  - Change for an active reconnect, with exponential backoff, would also include keep-alive measures
  - Introduce a pre-execute check that the connection is alive and ready to be used
- Make WS use read and write deadlines to avoid blocking
