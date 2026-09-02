# internal/e2e

End-to-end test suite that validates the full Golem ticket lifecycle from two angles: a unit-level integration test that exercises the Go API directly using a mock agent backend, and a binary-level test that builds the real golem executable, runs CLI commands against a temporary git repository, and verifies observable side-effects such as log entries and ticket state files. The module exists to catch regressions that only surface when all subsystems are wired together, covering workspace creation, ticket persistence, observer dispatch, and blog log correctness.

## Imports

github.com/leonpham/golem/internal/agentrunner, github.com/leonpham/golem/internal/blog, github.com/leonpham/golem/internal/observer, github.com/leonpham/golem/internal/ticket, github.com/leonpham/golem/internal/workspace
