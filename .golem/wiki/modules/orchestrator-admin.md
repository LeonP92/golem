# orchestrator/admin

Package `internal/orchestrator/admin` provides the admin operations that work directly against the GORM database (no HTTP server required):

- `UsersAdd(gdb, username, role)` — validates `role` via `rbac.ParseRole` (returning `ErrUnknownRole`), prompts for a password via terminal, bcrypt-hashes it, and inserts a `db.User` row.
- `UsersAddOrUpdate(gdb, username, password, role)` — upsert used by the `GOLEM_ADMIN_PASSWORD` auto-provision path; on update it sets both password hash and role.
- `UsersList(gdb)` — all users ordered by username.
- `UsersSetRole(gdb, username, role)` — changes a role; returns `ErrLastAdmin` rather than demoting the last admin.
- `UsersRemove(gdb, username)` — deletes the user *and their `db.Session` rows*, so access is revoked immediately. Returns `ErrLastAdmin` rather than removing the last admin, and errors if the user does not exist.
- `ShemsAdd(gdb, name)` — generates a random 64-char hex API key, stores its bcrypt hash in a new `db.Shem` row (status `offline`, repos `[]`), and prints the raw key once to stdout.
- `ShemsRemove(gdb, name)` — hard-deletes the shem by name.

These are wired into `cmd/orchestrator/main.go` as CLI subcommands:

```
orchestrator users add <username> [--role admin|developer]
orchestrator users list
orchestrator users set-role <username> <role>
orchestrator users remove <username>
orchestrator shems add --name <name>
orchestrator shems remove <name>
```

When a subcommand is detected, the binary opens the database (via `ORCHESTRATOR_DB` env var or `orchestrator.db` fallback), runs the operation, and exits without starting the HTTP server.

The last-admin guards (`wouldOrphanAdmins`) and the guards in `db.ensureAdmin` are the only places outside `orchestrator-rbac` that name a role literal — they compare against `string(rbac.RoleAdmin)` in a SQL `role = ?` count. Everything else asks `rbac.Can`.

Depends on: `golang.org/x/term` (for `term.ReadPassword`), `golang.org/x/crypto/bcrypt`, `internal/orchestrator/db`, `internal/orchestrator/rbac`.
