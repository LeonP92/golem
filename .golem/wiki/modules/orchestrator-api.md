# orchestrator/api

`internal/orchestrator/api` — HTTP handler package for the Golem orchestrator's shem and ticket REST endpoints.

## What it does

- `Handlers` struct holds `*gorm.DB`, `*ws.Hub`, and `*sse.Broker`.
- `NewHandlers(gdb, hub, broker)` constructs a Handlers.
- `RegisterShemRoutes(mux)` wires:
  - `POST /api/shems/register` (API-key auth) — updates shem status to online, normalizes repos, returns `{shem_id}`
  - `DELETE /api/shems/me` (API-key auth) — marks shem offline and unregisters from WS hub
  - `GET /api/shems` (session auth) — lists all shems
  - `GET /api/ws` (API-key auth) — WebSocket upgrade, registers conn in hub
- `RegisterTicketRoutes(mux)` wires:
  - `POST /api/tickets` (session auth) — creates a ticket
  - `GET /api/tickets` (session auth) — lists tickets
  - `GET /api/tickets/{id}` (session auth) — ticket detail + log entries
  - `GET /api/tickets/available?repo=` (API-key auth) — lists unassigned tickets
  - `POST /api/tickets/{id}/claim` (API-key auth) — atomic claim via `ClaimTicket`
  - `POST /api/tickets/{id}/revise-claim` (API-key auth) — resumes a ticket already owned by the calling shem in the `revising` phase, via `ReviseClaim`; does not mutate `phase`/`assigned_shem` (already owned) and always returns `checkpoint_phase: "revising"`
  - `PATCH /api/tickets/{id}/phase` (API-key auth) — updates ticket phase (owner only)
  - `PATCH /api/tickets/{id}/checkpoint` (API-key auth) — updates checkpoint phase/SHA (owner only)
- `RegisterLogRoutes(mux)` wires:
  - `POST /api/tickets/{id}/log` (API-key auth) — appends a log entry with server-assigned sequence_num; SPEC/PLAN types are upserted (re-submission replaces existing row); auto-creates `HumanInput` when `to_role=human`; publishes to SSE broker; returns `{"sequence_num": N}`
  - `GET /sse/tickets/{id}/log` (session auth) — SSE stream of `LogEntryEvent` JSON blobs
- `RegisterHumanRoutes(mux)` wires the consolidated human-input REST API:
  - `POST /api/tickets/{id}/human-inputs` (API-key auth) — create a HumanInput; body `{"kind": "approval"|"feedback"|"question_answer"|"blocker_ack", "prompt": "..."}`; returns 201
  - `GET /api/tickets/{id}/human-inputs` (API-key auth) — list inputs; optional `?kind=<kind>` and `?resolved=false` filters; always returns an array
  - `PATCH /api/tickets/{id}/human-inputs/{inputID}` (API-key auth) — resolve an input; body `{"response": "..."}`
  - `POST /api/tickets/{id}/actions` (session auth) — single dispatcher for all human-initiated actions; body `{"action": "approve"|"requeue"|"close"|"needs-attention"|"request-changes"|"answer", "feedback": "...", "input_id": N, "response": "..."}`. `request-changes` branches on the ticket's phase: on `brainstorm`/`plan` it resolves the pending `approval` HumanInput as before; on `ready-for-review` (`requestChangesFromReview`) there is no pending approval to resolve — it instead moves `phase` to `revising` and pushes a `ticket_revise` `ws.WSMessage` to the ticket's `assigned_shem` (409 if none assigned) so the owning shem can resume work

`POST /api/tickets`, `GET /api/tickets`, and `GET /api/tickets/{id}` responses wrap `db.Ticket` in a `ticketResponse` that adds a resolved `created_by` username field (via `db.CreatorNames`). `createTicket` sets `CreatedByUserID` from `auth.SessionUser(r)` server-side — a client-supplied value in the request body is ignored.

## Why it exists

Provides the full REST surface that shem workers and the UI consume. Atomic claim prevents two shems from grabbing the same ticket under concurrent requests. Log append uses upsert semantics for SPEC/PLAN so re-runs replace the prior doc rather than accumulating duplicates. Human-input rows decouple blocking questions from normal log flow; the `request-changes` action closes the approval input and injects a `feedback` input that the shem picks up on its next iteration.
