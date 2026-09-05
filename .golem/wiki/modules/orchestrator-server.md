# orchestrator/server

`internal/orchestrator/server` — HTTP server scaffold for the Golem orchestrator.

## What it does

`New(gdb, hub, broker) *Server` creates a Server holding the shared `*gorm.DB`, `*ws.Hub`, and `*sse.Broker`. `Routes() http.Handler` returns the full mux with all handler groups registered.

Registered route groups:
- `RegisterShemRoutes` — shem registration, WS upgrade
- `RegisterTicketRoutes` — ticket CRUD, claim, phase/checkpoint updates
- `RegisterLogRoutes` — log ingestion (API key), SSE streaming (session)
- `RegisterHumanRoutes` — pending/ack, plus UI action endpoints: approve, answer, requeue, close, needs-attention
- `ui.Handlers.RegisterRoutes` — browser-facing dashboard pages (login, dashboard, shems, ticket detail/new)

UI action endpoints (`internal/orchestrator/api/human.go`):
- `POST /api/tickets/{id}/approve` — resolves oldest `approval` HumanInput, advances brainstorm→plan or plan→implement, pushes WS `human_input_resolved`
- `POST /api/tickets/{id}/answer` — resolves a `question_answer` by `input_id`, writes `ANSWER` log, pushes WS + SSE
- `POST /api/tickets/{id}/requeue` — transitions `needs-attention→unassigned`, clears assigned shem, broadcasts `ticket_available`
- `POST /api/tickets/{id}/close` — transitions `ready-for-review→closed`
- `POST /api/tickets/{id}/needs-attention` — manually flags any ticket as `needs-attention`

## Why it exists

Provides a single place to wire together the database, WebSocket hub, and SSE broker, and to register all HTTP routes.
