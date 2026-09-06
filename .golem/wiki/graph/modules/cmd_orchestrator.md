# cmd/orchestrator

This is the main entry point for the orchestrator server binary. It handles two distinct execution modes: an admin CLI mode for managing users and shem API keys directly against the database (subcommands `users add/remove` and `shems add/remove`), and a normal HTTP server mode that loads configuration, auto-provisions admin and shem credentials from environment variables, wires together the WebSocket hub, SSE broker, and heartbeat monitor, then listens for connections with optional TLS.

## Imports

context, fmt, log, net/http, os, time, github.com/leonp92/golem/internal/orchestrator/admin, github.com/leonp92/golem/internal/orchestrator/config, github.com/leonp92/golem/internal/orchestrator/db, github.com/leonp92/golem/internal/orchestrator/server, github.com/leonp92/golem/internal/orchestrator/sse, github.com/leonp92/golem/internal/orchestrator/ws
