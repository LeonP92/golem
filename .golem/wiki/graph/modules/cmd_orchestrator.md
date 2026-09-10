# cmd/orchestrator

This is the main entry point for the orchestrator server binary. It handles two distinct execution modes: an admin CLI mode for managing users and shem API keys directly against the database via subcommands "users add/remove" and "shems add/remove", and a normal HTTP server mode that loads YAML configuration, opens the database, auto-provisions an admin user and a shem API key from environment variables (GOLEM_ADMIN_PASSWORD/GOLEM_ADMIN_USERNAME and GOLEM_SHEM_API_KEY/GOLEM_SHEM_NAME) on every startup, wires together the WebSocket hub, SSE broker, and a background heartbeat monitor, and then listens for HTTP or HTTPS connections depending on whether TLS certificate and key paths are configured.

## Imports

context, fmt, log, net/http, os, time, github.com/leonp92/golem/internal/orchestrator/admin, github.com/leonp92/golem/internal/orchestrator/config, github.com/leonp92/golem/internal/orchestrator/db, github.com/leonp92/golem/internal/orchestrator/server, github.com/leonp92/golem/internal/orchestrator/sse, github.com/leonp92/golem/internal/orchestrator/ws
