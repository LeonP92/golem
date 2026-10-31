# Bounded attention recovery

`internal/orchestrator/api/attention.go` keeps eligibility, attempt counts,
normalized failure fingerprints and dispatch leases on the existing ticket.
Only an explicit failure source opts a ticket in. Human parks, legacy rows,
exhausted PR/epic revision loops and non-repairable lane errors remain parked.

The recovery read is a filter on the ticket list: `GET /api/tickets?attention=true` (API-key auth) returns the calling shem's recoverable tickets due now, in the same shape as every list response. `GET /api/tickets/attention` remains as a deprecated alias for shems built before the filter moved. Attention claim/result requests require the owning shem, unchanged intake
approval, an active epic revision, a checkpoint and a matching unexpired
dispatch token. Feedback and phase change commit together. The ticket remains
assigned so the existing retry reconstruction preserves dirty work. Human
phase changes invalidate recovery tokens. Counts are durable across restart.

Stale claims return conflict; storage failures return server errors and roll
back recovery state and feedback together.

## Supervisor waits

A resolve names exactly one outcome: `retry`, `wait` (`transient` or
`usage_limit`) or a park, with a `decision` record riding its retry in the same
transaction. The server owns the wait policy; the shem only classifies the
failure and, for a usage limit, passes the `reset_at` it parsed. A wait is
bound to the failure fingerprint (clock times are stripped from it, so one limit
repeating is one incident). Transient: five checks in thirty minutes, 1/2/4/8/15
minute backoff. Usage limit: until `reset_at` plus 60-120 s jitter, capped by
`supervisor.usage_limit_max_wait` (8h); with no parsable reset, the
`supervisor.limit_backoff` schedule (15m, 30m, 1h, 2h) inside
`supervisor.limit_backoff_max` (6h). After the budget a repeat of the failure
falls to the same-failure-twice park. A due wait claims as `wait_resume` without
spending an attempt and retries the saved step as-is.

A usage-limit wait also records `shems.limited_until` on the owning shem (max
of existing). `GET /api/shems/me` returns the caller's own row, that value
included, so a restart, a human retry/requeue (which deletes the wait) or a ticket moving away
cannot lift the hold early. The scope is the shem, not the backend: nothing
identifies which account a shem signs in with, so sibling shems of one backend
are not held (they hit the limit themselves and then hold). A shem also reports any usage limit it sees with `POST /api/backend-limit` (own row, later only, capped at `usage_limit_max`), so it holds across a restart even when no wait follows (for example a `manual_approval` ticket). The server also refuses `ClaimTicket` and returns an empty `available` list to a shem whose limit is in the future, so a stale or restarted client cannot take work. `GET /api/shems` adds a computed `limit` ({kind: usage_limit, backend, until}) and the shems page shows "usage limit until <UTC time>" instead of online/offline, both only while the limit is in the future. `POST /api/tickets/{id}/release` returns a `claimed`, unstarted
ticket (no checkpoint) to the pool. A human `retry-current-step` or `requeue`
resets the attempt counter, repeat detector and wait (`resetAttention`).
Tickets with `manual_approval` are outside every automatic route; the display
phase reads "recovering" while a wait is open.

## Moving a limit-waiting ticket

A shem held by its own usage limit may hand a ticket that is waiting out that limit to the pool. `GET /api/tickets?migratable=true` lists the caller's own tickets with a future `usage_limit` wait, not `manual_approval`, not pinned (`model_backend=''`), not moved before (`migrations=0`) and with a checkpoint. `POST /api/tickets/{id}/migrate` `{checkpoint_sha, captured_sha?}` refuses (409) another checkpoint, a stale owner or an ineligible ticket. On success the ticket is unassigned with `retry_phase` kept, the attempt count untouched (a limit never spends one: `resolveWait` gives back exactly the attempt its own claim spent, unless the claim continued an open incident), the failure fingerprint reset (the same limit message on the new shem is a first failure there, not "same failure twice"), `checkpoint_sha` replaced by `captured_sha` when the shem captured unfinished work, the wait and token cleared, `migrated_backend` set to the owner's backend and `migrations` to 1. Claiming is limited to shems of `migrated_backend`, so the move never crosses providers. Any non-held same-backend shem may try it; that is not a claim that it has quota, and no credentials are inspected. If none is free the ticket stays claimable, and the origin can reclaim it once its hold ends, so it is never stranded. A move happens once per ticket; a human retry or requeue resets `migrations` and `migrated_backend`. The shem pushes the work first; the server only records the commit the shem names.
