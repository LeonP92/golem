# shem/worker

`internal/shem/worker` — Shem claim loop, executor, and graceful shutdown.

## What it does

**Worker** (`worker.go`) polls `GET /api/tickets/available` every 10 s (and reacts to WS `ticket_available` pushes) to find unclaimed tickets. For each candidate it calls `POST /api/tickets/{id}/claim`; on 409 it continues silently (another shem won). On success it runs the ticket via an `Executor` interface.

- `running map[uint]context.CancelFunc` tracks active tickets by ID; a per-ticket cancel is stored there after claim, replacing an idle placeholder that prevents duplicate-claim races.
- `tryClaimAndRun(ticketID)` — deduplicates by ticket ID, enforces `cfg.MaxConcurrent`, claims, then runs the executor in a goroutine.
- `HandleMessage` also reacts to WS `ticket_revise` pushes (sent when a human requests changes on a `ready-for-review` ticket) by calling `tryReviseAndRun(ticketID)` — same dedupe/slot/cleanup structure as `tryClaimAndRun`, but calls `POST /api/tickets/{id}/revise-claim` (`client.ClaimRevision`) instead of `/claim`, since the ticket is already owned by this shem rather than unassigned. A 409 (requeued before reconnect) is a silent no-op. It also reacts to `epic_resumed` by re-running the integration lane for assigned children still waiting on their merge (`integrateAssigned`).

Graceful shutdown: `Shutdown()` cancels all running tickets and spin-waits up to 10 minutes for the map to drain, then calls `Deregister`.

`cleanupTicket` (invoked on `ticket_closed`) reads the ticket's local `state.json` (via `ticket.Load`) to get the real working branch before calling `workspace.Remove`, instead of re-deriving `"ticket/" + ticketID` — branches are now `ticket/<slug>-<id8>`, so the old re-derivation would target a nonexistent ref and leak the branch.

**GolemExecutor** (`executor.go`) runs a claimed ticket by:
1. Resolving the local repo path from `config.Repos` by normalized remote URL.
2. Starting a `tailLog` goroutine that polls `.golem/tickets/<id>/log.jsonl` every 500 ms and POSTs new JSON-lines to `/api/tickets/{id}/log`.
3. Resuming from a completed checkpoint at the next unfinished phase, or from an explicit retry phase with `RecoverTicketForRetry`; startup recovery preserves that phase rather than moving the orchestrator back to the checkpoint.
4. Driving the brainstorm → plan → implement → review lifecycle via `golem ticket new` / `golem ticket resume`. `golem ticket new` is invoked with `--branch claim.Branch` so the worktree is created on the server-computed slug branch instead of `golem ticket new`'s own `ticket/<id>` default. When the claim carries a `BaseBranch`, the worker fetches it and pins it to an exact commit (`resolveBaseSHA`), passing `--base <sha>`. With no_push or a `local://` remote nothing is fetched. If the base cannot be resolved, an ordinary ticket starts from HEAD (the pre-pinning behavior) and an epic child falls back to the run's base branch. Brainstorm and plan phases loop: run Claude, post approval request, wait; if a `feedback` HumanInput is pending after approval, the loop re-runs the phase with the feedback injected into the prompt.
5. A `"revising"` phase case (reached only via `ReviseClaim`'s `CheckpointPhase: "revising"` sentinel, not a local ticket phase): consumes the pending `feedback` HumanInput, runs `buildRevisePrompt` in the existing worktree/branch (no plan restatement, no `golem ticket close`), then re-reads local state and posts the resulting phase (`ready-for-review` or `needs-attention`) back — same checkpoint/post-phase pattern as the `implement` case.
6. After the CLI returns, reading `.golem/tickets/<id>/state.json` and posting a final checkpoint via `PostCheckpointWithRetry`.

The `tailDone` channel ensures the tail goroutine fully exits (flushing its last drain pass) before `RunTicket` returns, preventing file-handle leaks on Windows.

**Recovery** (`recover.go`) restores a checkpointed ticket's local worktree after a node failure:
- `RecoverTicket(ctx, claim, cfg)` — clones repo if absent, fetches, checks out `claim.Branch` (the server-computed slug branch carried through the claim, not re-derived from the ticket ID), resets to checkpoint SHA, then calls `ReconstructState`.
- An explicit current-step retry uses `RecoverTicketForRetry`: it keeps the existing worktree when it is already on the ticket branch (including uncommitted edits), refuses to reset an existing wrong-branch or detached worktree, and restores from the saved SHA only when the worktree is absent.
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

**Agent** (`agent.go`) is the adapter this shem runs plus the `backend.yaml`
its nested `golem` subcommands read. `NewAgent` builds the configured adapter
and writes that document into a temporary run directory, returning a cleanup
func. `GolemExecutor.claimModel` returns the model the claim resolved for a
stage, used by the phases and by every agent-invoking subcommand; with no
models on the claim it falls back to `StageModel`, this shem's own default.
`subcommandArgs` appends `--backend-config` and `--model`. `runPhase` runs one
phase through the adapter, teeing output to `agent-<phase>.log`.

`agentlog.go` holds one posting helper, `postLog`, with `postStatus`,
`postWarning` and `postPhaseStart` wrapping it. `postPhaseStart` records the
backend and model on the entry. `sanitizeModel` returns a model id the adapter
can use, or `""` with a WARNING so the stage runs on the vendor default.

`finish.go` holds `finishWorkPhase`, moved out of `executor.go` unchanged.

## Epic children on the worker

The claim's optional `epic` block (`EpicClaim`) carries run id, node key,
base fallback, `tester_required` and the node's acceptance
commands.

- Scaffold: the base resolves from the child's branch target; while the
  run's `epic/<id>` ref does not exist yet, `resolveBaseSHA` uses the run's base branch. Acceptance commands go to
  `golem ticket new --accept`, so verify and review run them after
  `gate.commands`.
- Finish: the branch is pushed unless no_push (a push failure is
  non-fatal), the first child also creates the integration ref at the
  pinned base SHA if missing (`ensureRemoteBaseRef`, never advances an
  existing one), and the branch is reported. Then, in order: the phase
  lands (the verdict handler bounces a failed child out of
  ready-for-review, so the child must be there first), the tester stage
  runs, and for an epic child this shem runs the merge lane itself
  (`integrate.go`).
- Tester stage (`tester.go`), gated by `tester_required` (a ticket-level
  flag — the claim's top-level field, with the epic block's flag as the
  fallback for children materialized before it existed): runs the
  `tester` role against the measured worktree head and posts the verdict —
  epic children to `POST /api/epics/{id}/verdicts`, standalone tickets to
  `POST /api/tickets/{id}/tester-verdict` — downgraded to inconclusive if
  the agent reports another head or the head moves. Tester failures are
  reported and swallowed; the lane waits.
- Integration lane (`integrate.go`): pin the head from the lane remote
  (`integrationRemote`: the claim's remote, or the local repository
  itself under no_push / `local://` — the same place no_push routes the
  agent's own pushes, so children merge into the epic branch locally with
  no remote or credential), pass the gate (`CheckIntegrationGate`, which
  grants the per-run slot), merge into the run's target ref via
  `forge.Forge` in the push mirror (wired in `cmd/shem`), post the result. `wait` retries with
  the gate's hint, bounded (`maxIntegrationWaits`, shared with moved-pin
  re-runs — a branch that never settles IS the lane staying busy). Every
  step is idempotent, so recovery is simply running the lane again:
  `IntegrateAssigned` re-runs it from the startup sweep and the
  `epic_resumed` WS push for assigned children still at ready-for-review.
- Self-verification (`golem ticket verify`) runs before the review gate for
  every ticket, unless the ticket opted out at creation (`verify_disabled`);
  a failed check bounces the implementer once with the output.

## Bounded attention recovery

`attention.go` adds the `Diagnoser` interface and its `ModelDiagnoser`
implementation over the configured backend's role runner. The host-owned
per-repo policy defaults on with a three-dispatch lifetime budget. Known
operational errors bypass inference. Structured retry/human output is checked
before the worker asks the server to resume a failed phase with feedback.
For an autonomous ticket the contract adds a `decide` route carrying a
`{question, choice, reason, basis}` decision: the worker posts it as a
`DECISION` log entry and retries like `retry` (same attempt budget); for an
ask ticket `decide` parks as `human`. Failure text naming a hard limit
(weakening a gate, a protected file, real money or providers) bypasses
inference like the operational errors.
The existing running map enforces worker slots. Dirty branch state is kept;
unchanged failed code repairs stop, while spec/plan repair can progress without
a commit. Worker output tails preserve usage/auth messages in the failure.
Server fencing, counters and eligibility are in `api/attention.go`.

Supervisor routes run before diagnosis: a due `wait_resume` claim retries the
saved step without a new diagnosis; `classifyFailure` (limits.go) sends a usage
limit or transient fault to a server-owned `wait` (a usage limit carries the
reset time `parseLimitReset` parsed), and a refused wait escalates once to a
human park. Tickets with `manual_approval` never enter these routes.

Classification is by wording, never by backend name, and reads only the last
4 KB of a reason (the agent's own error comes last; a reviewer's diff or gate
output above it may mention limits as text). A "review gate did not pass"
reason is a verdict, so only its first line counts. A usage limit is a
usage/session/weekly/daily/monthly/hourly/plan limit; a transient fault is a
rate limit, overload, gateway or connection fault, or a status code written as
one (`status 503`), never a bare number or "timeout" (`TestTimeout`,
`file.go:429`). `parseLimitReset` reads the last limit message and tries, in
order: a delay ("in 3 hours 20 minutes", "retry after 90 seconds"), an ISO
timestamp, a dated clock time ("Nov 4th, 2026 12:39 PM"; no year means the next
such date) and a bare 12- or 24-hour clock time (its next occurrence). A
parenthesised zone applies to the time before it, else the shem's zone; an
unknown zone or no time reports false and the server backs off instead.

The limit breaker (`limitBreaker`, per backend, injectable clock) holds the
shem when its backend is limited: no new claims, resumes, revisions or
diagnoses, while waits and `wait_resume` still run. It trips locally when a
phase fails with a usage limit, and `syncLimit` re-learns it from
`GET /api/shems/me` each poll, so it survives a restart. A ticket claimed as
the breaker tripped is released unstarted.

Startup and steady-state attention polling share `pollAttention`; failed reads
are logged rather than silently treated as an empty queue.

## Plan jobs (planjob.go)
`tryClaimPlanJob` claims an epic plan-generation job (pushed as an
`epic_plan` message, or found by the periodic `GetAvailablePlanJob` poll) and
`runPlanJob` runs one agent session in the repo checkout at the `epic-plan`
stage model, writing `.golem/epic-plan/<job>/plan.json`, which is removed when
the run ends. The draft is validated with `epic.CanonicalPlan`; an invalid
draft gets one repair session seeded with the errors. A heartbeat posts a
bounded, mutex-guarded `tailWriter` of the session log every minute to keep
the lease fresh. Every exit posts a result, and an agent error posts its error
text plus the log tail so it never reads as success.

## Autonomous decisions (prompts.go)
`decisionBlock` adds the autonomous-mode text to the brainstorm and plan
prompts from the claim's `AskDecisions`/`DecisionGuidance` (empty guidance
= `defaultDecisionGuidance`): decide open questions, record each with
`golem log emit --type DECISION`, list them under a "Decisions" heading, and
leave the hard limits to a human. With `AskDecisions` it returns "" and the
prompts are unchanged. The reviewer role accepts a deviation a
recorded DECISION entry backs; the implement and revise prompts carry no
block.

## Worktree setup and redaction

`prepareWorktree` runs the repo's `worktree_setup` commands (`runSetup`, timeout
from `setup_timeout`, parsed once into a `time.Duration` by the shem config) and
records a marker, keyed on commands and base, in the worktree git dir. The active
worktree is secret-checked even without setup or with a cached marker, and again
after setup. A failed setup or environment check carries `evidence.EnvironmentMarker`, which
`humanOnlyFailure` treats as a stop for automatic repair; `finishWorkPhase` keeps
the command output after the marker. Prompts and phase output are redacted by the
agentrunner adapters only; the client redacts what it posts.

`migrateWaiting` (migrate.go) runs from the poll loop while the shem is held, with no model call. For each ticket from `Migratable()` (skipping `no_push`, `local://` and a ticket with a running agent, at most one attempt per ticket per 5 minutes) it: requires HEAD to be the checkpoint or a descendant of it; scans everything since the checkpoint with the evidence package's rules (`evidence.SensitivePath`, and `evidence.Clean` on the added lines, so credential assignments and private keys refuse; binary files refuse because they cannot be scanned); if the worktree has uncommitted work, commits it as one capture commit built in a private index (`git add -A`, so `.gitignore` still excludes files, no hooks, and a refusal leaves the user's own staging untouched); asks the server again whether the ticket is still migratable right before pushing; pushes HEAD to the ticket branch without force, through the Golem-owned mirror like `pushTicketBranch`, only when the remote tip is an ancestor of HEAD; confirms the remote holds HEAD; then calls `MigrateTicket` with the checkpoint it saw and the captured commit. Any failure leaves the ticket waiting here. A capture commit left by a crash is pushed on the next attempt. The capture does not advance the phase, so nothing reads as review-complete. Git runs with the agent environment (no push token); only the mirror push carries the credential. Residual risk: the scan is pattern-based, so a secret that matches no pattern and sits in a non-sensitive file name is not caught.
