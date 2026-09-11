# shem/worker

`internal/shem/worker` — Shem claim loop, executor, and graceful shutdown.

## What it does

**Worker** (`worker.go`) polls `GET /api/tickets/available` every 10 s (and reacts to WS `ticket_available` pushes) to find unclaimed tickets. For each candidate it calls `POST /api/tickets/{id}/claim`; on 409 it continues silently (another shem won). On success it runs the ticket via an `Executor` interface.

- `running map[uint]context.CancelFunc` tracks active tickets by ID; a per-ticket cancel is stored there after claim, replacing an idle placeholder that prevents duplicate-claim races.
- `tryClaimAndRun(ticketID)` — deduplicates by ticket ID, enforces `cfg.MaxConcurrent`, claims, then runs the executor in a goroutine.
- `HandleMessage` also reacts to WS `ticket_revise` pushes (sent when a human requests changes on a `ready-for-review` ticket) by calling `tryReviseAndRun(ticketID)` — same dedupe/slot/cleanup structure as `tryClaimAndRun`, but calls `POST /api/tickets/{id}/revise-claim` (`client.ClaimRevision`) instead of `/claim`, since the ticket is already owned by this shem rather than unassigned. A 409 (requeued before reconnect) is a silent no-op.

Graceful shutdown: `Shutdown()` cancels all running tickets and spin-waits up to 10 minutes for the map to drain, then calls `Deregister`.

`cleanupTicket` (invoked on `ticket_closed`) reads the ticket's local `state.json` (via `ticket.Load`) to get the real working branch before calling `workspace.Remove`, instead of re-deriving `"ticket/" + ticketID` — branches are now `ticket/<slug>-<id8>`, so the old re-derivation would target a nonexistent ref and leak the branch.

**GolemExecutor** (`executor.go`) runs a claimed ticket by:
1. Resolving the local repo path from `config.Repos` by normalized remote URL.
2. Starting a `tailLog` goroutine that polls `.golem/tickets/<id>/log.jsonl` every 500 ms and POSTs new JSON-lines to `/api/tickets/{id}/log`.
3. When `CheckpointPhase != nil`, calling `RecoverTicket` to restore local worktree state before resuming.
4. Driving the brainstorm → plan → implement → review lifecycle via `golem ticket new` / `golem ticket resume`. `golem ticket new` is invoked with `--branch claim.Branch` so the worktree is created on the server-computed slug branch instead of `golem ticket new`'s own `ticket/<id>` default. Brainstorm and plan phases loop: run Claude, post approval request, wait; if a `feedback` HumanInput is pending after approval, the loop re-runs the phase with the feedback injected into the prompt.
5. A `"revising"` phase case (reached only via `ReviseClaim`'s `CheckpointPhase: "revising"` sentinel, not a local ticket phase): consumes the pending `feedback` HumanInput, runs `buildRevisePrompt` in the existing worktree/branch (no plan restatement, no `golem ticket close`), then re-reads local state and posts the resulting phase (`ready-for-review` or `needs-attention`) back — same checkpoint/post-phase pattern as the `implement` case.
6. After the CLI returns, reading `.golem/tickets/<id>/state.json` and posting a final checkpoint via `PostCheckpointWithRetry`.

The `tailDone` channel ensures the tail goroutine fully exits (flushing its last drain pass) before `RunTicket` returns, preventing file-handle leaks on Windows.

**Recovery** (`recover.go`) restores a checkpointed ticket's local worktree after a node failure:
- `RecoverTicket(ctx, claim, cfg)` — clones repo if absent, fetches, checks out `claim.Branch` (the server-computed slug branch carried through the claim, not re-derived from the ticket ID), resets to checkpoint SHA, then calls `ReconstructState`.
- `ReconstructState(ticketDir, claim)` — writes `state.json` (phase/branch/sha) and `log.jsonl` from the `ClaimResponse`. Idempotent.
- `CloneIfMissing(ctx, repoPath, remote)` — runs `git clone` only if `repoPath` does not exist.

**Checkpoint** (`checkpoint.go`):
- `PostCheckpointWithRetry(c, ticketID, phase, sha, maxAttempts)` — temporarily overrides `c.RetryAttempts` and calls `c.PostCheckpoint`, restoring the original value on return.

## Why it exists

Decouples ticket execution from the orchestrator so multiple Shem processes can run on different machines, each claiming tickets for repos they have checked out. The `running` map (replacing the prior `active bool`) enables true parallel ticket execution within a single Shem process. The feedback loop lets humans request changes to a spec, plan, or (via the `revising` phase) an already-reviewed implementation, and have Claude revise it in place without a full requeue.

## Dependencies

- `internal/shem/client` — HTTP client for claim/phase/checkpoint/log calls
- `internal/shem/config` — repo path/remote mapping
- `internal/orchestrator/ws` — `WSMessage` type for push handler
