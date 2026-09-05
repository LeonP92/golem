# orchestrator/ws

`internal/orchestrator/ws` — WebSocket hub and heartbeat monitor for the Golem orchestrator.

## What it does

- `Hub` — maintains a map of shem ID → WebSocket connection. `Register`, `Unregister`, `Push` (to one shem), and `Broadcast` (to all shems matching a repo remote URL).
- `WSMessage` — JSON-serialisable message type with `Type`, `TicketID`, `Repo`, `InputID`, `Response`.
- `StartHeartbeatMonitor(gdb, hub, checkInterval, timeout)` — goroutine that periodically finds shems with `last_heartbeat < now-timeout`, marks them offline, returns their tickets to `unassigned`, and unregisters them from the hub.

## Why it exists

Provides the real-time communication channel between the orchestrator server and shem worker processes, and automatically recovers from dead shems.
