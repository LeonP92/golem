# internal/e2e

End-to-end test suite that validates the full Golem ticket lifecycle from three angles: a unit-level integration test exercising the Go API directly with a mock agent backend, a binary-level test that builds the real golem executable and runs CLI commands against a temporary git repository, and an orchestrator test that exercises concurrent ticket claiming, heartbeat-based shem recovery, and SSE log forwarding using an in-memory SQLite database. The module exists to catch regressions that only surface when all subsystems are wired together, covering workspace creation, ticket persistence, observer dispatch, blog log correctness, and orchestrator coordination primitives.

## Functions

- TestCLILifecycleThroughRealBinary
- TestFullTicketLifecycleWithMockBackend
- TestConcurrentClaim_ExactlyOneWinner
- TestHeartbeatMonitor_RequeuesAndNewShemClaims
- TestLogForwarding_SSEDeliversInSequenceOrder

## Imports

bytes, encoding/json, os, os/exec, path/filepath, runtime, strings, testing, github.com/leonp92/golem/internal/agentrunner, github.com/leonp92/golem/internal/blog, github.com/leonp92/golem/internal/observer, github.com/leonp92/golem/internal/ticket, github.com/leonp92/golem/internal/workspace, context, fmt, net/http, net/http/httptest, sync, time, golang.org/x/crypto/bcrypt, gorm.io/gorm, github.com/leonp92/golem/internal/orchestrator/api, github.com/leonp92/golem/internal/orchestrator/db, github.com/leonp92/golem/internal/orchestrator/sse, github.com/leonp92/golem/internal/orchestrator/ws
