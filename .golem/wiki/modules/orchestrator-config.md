# orchestrator/config

`internal/orchestrator/config` — reads the orchestrator server configuration from a YAML file.

## What it does

Exposes `Load(path string) (*Config, error)` which reads a YAML file and returns a `*Config` struct containing `Port`, `DBPath`, `SessionSecret`, and `TLS` (cert + key paths). Used by `cmd/orchestrator/main.go` to bootstrap the server.

## Why it exists

Centralises all orchestrator runtime configuration so the binary can be parameterised without recompilation.
