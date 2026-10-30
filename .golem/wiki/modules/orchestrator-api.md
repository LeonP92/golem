# orchestrator/api

`internal/orchestrator/api` — HTTP handler package for the Golem orchestrator's shem and ticket REST endpoints.

Every "(session auth)" route below is additionally wrapped in `rbac.Require(...)` inside `auth.RequireSession` — `shem:view` for `GET /api/shems`, `ticket:create` for `POST /api/tickets`, `ticket:view` for the ticket reads and the SSE log stream, `ticket:manage` for `POST /api/tickets/{id}/actions` (see `orchestrator-rbac`). API-key (shem) routes are deliberately unwrapped: shems are machine principals with their own trust model and hold no role. A new session route without an `rbac.Require` is a bug.

## What it does

- `Handlers` struct holds `*gorm.DB`, `*ws.Hub`, and `*sse.Broker`.
- `NewHandlers(gdb, hub, broker)` constructs a Handlers.
- `RegisterShemRoutes(mux)` wires:
  - `GET /api/shems/me` (API-key auth) — the calling shem's own row, including the computed `limit` status
  - `POST /api/backend-limit` (API-key auth) — the shem reports a usage limit it saw; later-only, capped at `usage_limit_max`
  - `PUT /api/shems/me` (API-key auth) — idempotent self-update: sets shem status to online, normalizes repos, returns `{shem_id}`. PUT because a shem restart safely resends the same body.
  - `DELETE /api/shems/me` (API-key auth) — marks shem offline and unregisters from WS hub
  - `GET /api/shems` (session auth) — lists all shems
  - `GET /api/ws` (API-key auth) — WebSocket upgrade, registers conn in hub
- `RegisterTicketRoutes(mux)` wires:
  - `POST /api/tickets` (session auth) — creates a ticket
  - `GET /api/tickets` — lists tickets. Session auth: the full list. API-key auth (dispatched on the `X-Shem-Name` header): must pass `?attention=true` (the caller's recoverable needs-attention tickets due now) or `?migratable=true` (the limit-waiting subset it may move), which keep ownership and eligibility checks on the server; an unfiltered shem call is rejected. `GET /api/tickets/attention` is a deprecated alias of the attention filter, kept for older shems.
  - `POST /api/tickets/{id}/migrate` and `POST /api/tickets/{id}/release` (API-key auth) — hand a limit-waiting ticket to the pool, or give back a claimed unstarted one (see orchestrator-attention)
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

`createTicket` requires a `title` field (in addition to `repo_remote`, `branch`, `description`). It generates the ticket UUID up front so it can compute `Branch: slug.Branch(title, id)` before the single insert; the request's `branch` field is stored as `BaseBranch` (the branch the ticket forks from), not the working branch. `ClaimResponse` (returned by claim/resume/revise-claim) carries the computed `Title` and `Branch` through to the shem worker, so branch naming is computed once server-side rather than re-derived by each caller.

## Why it exists

Provides the full REST surface that shem workers and the UI consume. Atomic claim prevents two shems from grabbing the same ticket under concurrent requests. Log append uses upsert semantics for SPEC/PLAN so re-runs replace the prior doc rather than accumulating duplicates. Human-input rows decouple blocking questions from normal log flow; the `request-changes` action closes the approval input and injects a `feedback` input that the shem picks up on its next iteration.

## Model selection

`models.go` — `GET /api/models` (`listModels`) returns every backend's union
catalog and the stage list. `claimResponse` is the only place a `ClaimResponse`
is built, so `ClaimTicket`, `ReviseClaim` and `resumableTickets` all ship a
resolved `Backend` and one `Models` entry per stage. `resolveForShem` resolves a
ticket's selections against one shem's catalog; `warnOnce` logs a WARNING for
anything dropped, or for a ticket bound to another vendor, once per message.
`shemRow` loads a shem by id and errors when there is none.

The claim update requires `model_backend = '' OR model_backend = ?` and
`models = ?` (the selections the catalog check saw). A shem whose catalog lacks
a selected id cannot claim, and `availableTickets` omits the ticket.

`register` rejects a reported catalog that fails `Catalog.Validate`.

`setmodels.go` — `actionSetModels` replaces a ticket's selections on unclaimed,
non-closed tickets (404 unknown, 409 claimed or closed). `selectionColumns`
validates a submission and returns the two columns for the caller's own
`Updates`, so `start`, `requeue` and `request-changes` write selections in the
same statement as their transition. Request-changes checks only the submitted
values, against the assigned shem's own catalog.

## Epic routes (epics.go, epicgate.go)

Session + CSRF (writes need PermTicketCreate for create/revisions,
PermTicketManage for the rest): `POST/GET /api/epics`, `GET /api/epics/{id}`,
`POST /api/epics/{id}/revisions` (422 lists every validation problem;
an empty node `branch_target` defaults to the run's target ref),
`/approve` (exact revision + plan hash; 409 on a mismatch or a revision
that is not current), `/start` (idempotent), `/pause`, `/resume` (resume
also broadcasts `epic_resumed` so shems re-run lanes that waited out the
pause), `/merge-final` (the human attestation: the caller states the
integration branch is on base and names the resulting head SHA — Golem
never merges to base itself), and `/nodes/{key}/integrate` (manual
receipt, for repos without GitHub sync).

Errors map through `writeEpicResult` (404 / 422 invalid / 409 conflict / 500).
Approvals record the username (UI and API alike).

Shem API key: `POST /api/tickets/{id}/tester-verdict` records the tester
stage's verdict for a standalone ticket (same validated shape, ownership of
the ticket; epic children are refused — they report to their run).
Shem API key: `POST /api/epics/{id}/verdicts` — needs `epic_node_key` and
the calling shem must hold that node's ticket (`writeTicketOwnership`). A
`fail` verdict bounces the child to revising with the evidence as feedback
(`bounceIntegration`, capped by `GOLEM_FIX_ATTEMPTS`), an inconclusive one
parks it for a human; a pass needs nothing, the shem takes the lane.
Shem API key, same ownership rule: `POST .../nodes/{key}/integrate-check`
(the gate: preconditions plus the per-run slot; answers granted/wait/done)
and `POST .../nodes/{key}/integrate-result` (the receipt: merged/contained
records the integration, conflict bounces to revising with the conflicted
files, moved frees the slot, error parks the child).

Claim gates: `epicGatePool` (unassigned pool: running epic, approved
revision, `EpicReady`) and `epicGateResume` (resume/revise/reclaim:
running epic, approved revision) are ANDed into every ticket-serving query
and into ClaimTicket's conditional UPDATE. Ordinary tickets are
unaffected. The claim response carries the `epic` block the worker uses
(see shem-worker).

Only standalone tickets with a linked issue get a pull request:
`enqueuePRIfReady` requires an issue number, so epic children and unlinked
tickets produce no outbox activity. Children merge into the run's target
ref through the shem-side lane instead; the run's final pull request is
the add-on's hook (`ghsync.EnqueueFinalPR`).
