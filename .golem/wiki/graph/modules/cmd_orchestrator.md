# cmd/orchestrator

This is the main entry point for the orchestrator server binary. It handles two distinct execution modes: an admin CLI mode that operates directly on the database and exits, supporting role-aware user management ("users add [--role admin|developer]", "users list", "users set-role", "users remove") and shem API key registration ("shems add --name", "shems remove"); and a normal HTTP server mode that loads YAML configuration, opens the database, auto-provisions an admin user (with the admin role) and a shem API key from environment variables (GOLEM_ADMIN_PASSWORD/GOLEM_ADMIN_USERNAME and GOLEM_SHEM_API_KEY/GOLEM_SHEM_NAME) on every startup, wires together the WebSocket hub, SSE broker, and a background heartbeat monitor, derives the secure-cookie flag from whether TLS is configured, and then listens for HTTP or HTTPS connections accordingly.

## Imports

context, fmt, log, net/http, os, time, github.com/leonp92/golem/internal/orchestrator/admin, github.com/leonp92/golem/internal/orchestrator/config, github.com/leonp92/golem/internal/orchestrator/db, github.com/leonp92/golem/internal/orchestrator/rbac, github.com/leonp92/golem/internal/orchestrator/server, github.com/leonp92/golem/internal/orchestrator/sse, github.com/leonp92/golem/internal/orchestrator/ws
