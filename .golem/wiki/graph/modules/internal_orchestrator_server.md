# internal/orchestrator/server

The server module is the HTTP wiring layer for the Golem orchestrator. It holds shared dependencies (database, WebSocket hub, SSE broker) in a Server struct and assembles the full request mux by delegating to the api, ui, and ws subpackages. It exists as the single composition root that binds all handler groups — Shem-facing API routes, ticket and log endpoints, human dashboard routes, and a health check — into one http.Handler returned to main.

## Functions

- New
- Routes

## Types

- Server

## Imports

log, net/http, github.com/leonp92/golem/internal/orchestrator/api, github.com/leonp92/golem/internal/orchestrator/sse, github.com/leonp92/golem/internal/orchestrator/ui, github.com/leonp92/golem/internal/orchestrator/ws, gorm.io/gorm
