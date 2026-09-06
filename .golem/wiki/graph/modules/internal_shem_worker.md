# internal/shem/worker

The worker module implements the Golem shem (agent daemon) execution layer. It contains three cooperating components: a Worker that polls the orchestrator for available tickets, claims them with concurrency control, and dispatches them via a pluggable Executor interface; a GolemExecutor that drives each ticket through brainstorm → plan → implement phases by invoking `claude --print` sessions, posting checkpoints and approval requests to the orchestrator, and tailing the ticket log for real-time forwarding; and a RecoverTicket subsystem that reconstructs local ticket state from a ClaimResponse checkpoint so that shem restarts can resume mid-flight tickets without data loss.

## Functions

- PostCheckpointWithRetry
- TestPostCheckpointWithRetry_RetriesAndSucceeds
- TestPostCheckpointWithRetry_ExhaustsRetries
- TestPostCheckpointWithRetry_RestoresRetryAttempts
- RunTicket
- TestGolemExecutor_RepoNotFound
- TestGolemExecutor_LogTailForwardsEntries
- RecoverTicket
- ReconstructState
- CloneIfMissing
- TestReconstructState_WritesFiles
- TestReconstructState_Idempotent
- TestReconstructState_EmptyLogs
- New
- SetWSClient
- Start
- HandleMessage
- Shutdown
- TestWorker_ClaimsOnPush
- TestWorker_IgnoresNonAvailableMessages
- TestWorker_HandlesClaim409

## Types

- GolemExecutor
- Executor
- Worker

## Imports

fmt, github.com/leonp92/golem/internal/shem/client, net/http, net/http/httptest, testing, time, github.com/leonp92/golem/internal/shem/worker, bufio, context, encoding/json, errors, log, os, os/exec, path/filepath, strings, sync, github.com/leonp92/golem/internal/shem/config, github.com/leonp92/golem/internal/orchestrator/db, github.com/leonp92/golem/internal/orchestrator/ws
