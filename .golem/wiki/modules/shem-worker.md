# shem/worker

`internal/shem/worker` — Shem claim loop, executor, and graceful shutdown.

## What it does

**Worker** (`worker.go`) polls `GET /api/tickets/available` every 10 s (and reacts to WS `ticket_available` pushes) to find unclaimed tickets. For each candidate it calls `POST /api/tickets/{id}/claim`; on 409 it continues silently (another shem won). On success it runs the ticket via an `Executor` interface.

- `running map[uint]context.CancelFunc` tracks active tickets by ID; a per-ticket cancel is stored there after claim, replacing an idle placeholder that prevents duplicate-claim races.
- `tryClaimAndRun(ticketID)` — deduplicates by ticket ID, enforces `cfg.MaxConcurrent`, claims, then runs the executor in a goroutine.

Graceful shutdown: `Shutdown()` cancels all running tickets and spin-waits up to 10 minutes for the map to drain, then calls `Deregister`.

**GolemExecutor** (`executor.go`) runs a claimed ticket by:
1. Resolving the local repo path from `config.Repos` by normalized remote URL.
2. Starting a `tailLog` goroutine that polls `.golem/tickets/<id>/log.jsonl` every 500 ms and POSTs new JSON-lines to `/api/tickets/{id}/log`.
3. When `CheckpointPhase != nil`, calling `RecoverTicket` to restore local worktree state before resuming.
4. Driving the brainstorm → plan → implement → review lifecycle via `golem ticket new` / `golem ticket resume`. Brainstorm and plan phases loop: run Claude, post approval request, wait; if a `feedback` HumanInput is pending after approval, the loop re-runs the phase with the feedback injected into the prompt.
5. After the CLI returns, reading `.golem/tickets/<id>/state.json` and posting a final checkpoint via `PostCheckpointWithRetry`.

The `tailDone` channel ensures the tail goroutine fully exits (flushing its last drain pass) before `RunTicket` returns, preventing file-handle leaks on Windows.

**Recovery** (`recover.go`) restores a checkpointed ticket's local worktree after a node failure:
- `RecoverTicket(ctx, claim, cfg)` — clones repo if absent, fetches, checks out branch, resets to checkpoint SHA, then calls `ReconstructState`.
- `ReconstructState(ticketDir, claim)` — writes `state.json` (phase/branch/sha) and `log.jsonl` from the `ClaimResponse`. Idempotent.
- `CloneIfMissing(ctx, repoPath, remote)` — runs `git clone` only if `repoPath` does not exist.

**Checkpoint** (`checkpoint.go`):
- `PostCheckpointWithRetry(c, ticketID, phase, sha, maxAttempts)` — temporarily overrides `c.RetryAttempts` and calls `c.PostCheckpoint`, restoring the original value on return.

## Why it exists

Decouples ticket execution from the orchestrator so multiple Shem processes can run on different machines, each claiming tickets for repos they have checked out. The `running` map (replacing the prior `active bool`) enables true parallel ticket execution within a single Shem process. The feedback loop lets humans request changes to a spec or plan and have Claude revise it without manual requeue.

## Dependencies

- `internal/shem/client` — HTTP client for claim/phase/checkpoint/log calls
- `internal/shem/config` — repo path/remote mapping
- `internal/orchestrator/ws` — `WSMessage` type for push handler
