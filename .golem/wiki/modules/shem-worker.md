# shem/worker

`internal/shem/worker` — Shem claim loop, executor, and graceful shutdown.

## What it does

**Worker** (`worker.go`) polls `GET /api/tickets/available` every 10 s (and reacts to WS `ticket_available` pushes) to find unclaimed tickets. For each candidate it calls `POST /api/tickets/{id}/claim`; on 409 it continues silently (another shem won). On success it runs the ticket via an `Executor` interface.

Graceful shutdown: `Shutdown()` waits for any in-flight ticket to complete its current step, closes the stop channel, then calls `Deregister`.

**GolemExecutor** (`executor.go`) runs a claimed ticket by:
1. Resolving the local repo path from `config.Repos` by normalized remote URL.
2. Starting a `tailLog` goroutine that polls `.golem/tickets/<id>/log.jsonl` every 500 ms and POSTs new JSON-lines to `/api/tickets/{id}/log`.
3. When `CheckpointPhase != nil`, calling `RecoverTicket` to restore local worktree state before resuming.
4. Shelling out to `golem ticket new --ticket-id <id> <description>` (or `golem ticket resume --ticket <id>` when a `CheckpointPhase` is present).
5. After the CLI returns, reading `.golem/tickets/<id>/state.json` and posting a final checkpoint via `PostCheckpointWithRetry`.

The `tailDone` channel ensures the tail goroutine fully exits (flushing its last drain pass) before `RunTicket` returns, preventing file-handle leaks on Windows.

**Recovery** (`recover.go`) restores a checkpointed ticket's local worktree after a node failure:
- `RecoverTicket(ctx, claim, cfg)` — clones repo if absent, fetches, checks out branch, resets to checkpoint SHA, then calls `ReconstructState`.
- `ReconstructState(ticketDir, claim)` — writes `state.json` (phase/branch/sha) and `log.jsonl` (one line per `LogEntry.Message`) from the `ClaimResponse`. Idempotent.
- `CloneIfMissing(ctx, repoPath, remote)` — runs `git clone` only if `repoPath` does not exist.

**Checkpoint** (`checkpoint.go`):
- `PostCheckpointWithRetry(c, ticketID, phase, sha, maxAttempts)` — temporarily overrides `c.RetryAttempts` and calls `c.PostCheckpoint`, restoring the original value on return.

## Dependencies

- `internal/shem/client` — HTTP client for claim/phase/checkpoint/log calls
- `internal/shem/config` — repo path/remote mapping
- `internal/orchestrator/ws` — `WSMessage` type for push handler
