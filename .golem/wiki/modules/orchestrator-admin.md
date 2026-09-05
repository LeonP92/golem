# orchestrator/admin

Package `internal/orchestrator/admin` provides four admin operations that work directly against the GORM database (no HTTP server required):

- `UsersAdd(gdb, username)` — prompts for a password via terminal, bcrypt-hashes it, and inserts a `db.User` row.
- `UsersRemove(gdb, username)` — hard-deletes the user by username.
- `ShemsAdd(gdb, name)` — generates a random 64-char hex API key, stores its bcrypt hash in a new `db.Shem` row (status `offline`, repos `[]`), and prints the raw key once to stdout.
- `ShemsRemove(gdb, name)` — hard-deletes the shem by name.

These are wired into `cmd/orchestrator/main.go` as CLI subcommands:

```
orchestrator users add <username>
orchestrator users remove <username>
orchestrator shems add --name <name>
orchestrator shems remove <name>
```

When a subcommand is detected, the binary opens the database (via `ORCHESTRATOR_DB` env var or `orchestrator.db` fallback), runs the operation, and exits without starting the HTTP server.

Depends on: `golang.org/x/term` (for `term.ReadPassword`), `golang.org/x/crypto/bcrypt`, `internal/orchestrator/db`.
