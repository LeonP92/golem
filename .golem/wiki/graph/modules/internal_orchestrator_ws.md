# internal/orchestrator/ws

This module implements the WebSocket connection layer for the Golem orchestrator, providing a thread-safe Hub that tracks active shem connections keyed by shem ID, supports targeted Push and repo-scoped Broadcast message delivery, and runs a background heartbeat monitor that detects dead shems (those that have not sent a heartbeat within a configurable timeout), marks them offline in the database, and returns their in-progress tickets to the unassigned pool.

## Functions

- StartHeartbeatMonitor
- TestHeartbeatMonitor_RequeuesDeadShem
- NewHub
- Register
- Unregister
- Push
- Broadcast
- TestHubPush

## Types

- WSMessage
- Hub

## Imports

context, time, github.com/leonp92/golem/internal/orchestrator/db, gorm.io/gorm, testing, github.com/leonp92/golem/internal/orchestrator/ws, encoding/json, fmt, sync, github.com/gorilla/websocket, net/http, net/http/httptest, strings
