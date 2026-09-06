# internal/orchestrator/sse

The sse module implements a fan-out event broker for Server-Sent Events within the orchestrator. It defines a LogEntryEvent type carrying structured log entry data (sequence number, entry type, role pair, and message), and a Broker that maintains per-ticket subscriber channels. Clients subscribe to a ticket's event stream and receive a cancel function to unsubscribe; the broker publishes events to all registered subscribers for a given ticket, dropping events silently when a subscriber's buffer is full rather than blocking.

## Functions

- NewBroker
- Subscribe
- Publish

## Types

- LogEntryEvent
- Broker

## Imports

sync
