# cmd/shem

This is the entry point for the shem worker binary — a lightweight agent process that connects to a Golem orchestrator, receives task assignments, and executes them via a GolemExecutor. On startup it loads shem.yaml configuration, constructs an HTTP client and worker, then establishes a WebSocket connection to the orchestrator with exponential-backoff reconnection logic, falling back gracefully to polling if WebSocket is unavailable. The process runs until it receives SIGTERM or SIGINT, at which point it shuts down cleanly.

## Imports

flag, log, os, os/signal, strings, syscall, time, github.com/leonp92/golem/internal/shem/client, github.com/leonp92/golem/internal/shem/config, github.com/leonp92/golem/internal/shem/worker
