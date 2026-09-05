# orchestrator/sse

`internal/orchestrator/sse` — Server-Sent Events broker stub for the Golem orchestrator.

## What it does

`Broker` fans out `LogEntryEvent` values to per-ticket subscribers. `Subscribe(ticketID)` returns a buffered channel and a cancel function. `Publish(ticketID, evt)` sends to all subscribers, dropping events if the buffer is full. Full SSE HTTP handler wired in Task 5.

## Why it exists

Enables the orchestrator web UI to stream ticket log entries in real time without polling.
