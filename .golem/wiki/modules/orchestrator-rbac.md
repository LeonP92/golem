# orchestrator/rbac

Package `internal/orchestrator/rbac` is the orchestrator's authorization policy: `Permission` constants, a static `map[Role][]Permission` table, `Can(u *db.User, p Permission) bool`, `ParseRole(s) (Role, bool)`, `Roles()` (stable order, for UI dropdowns and CLI error messages), and `Require(p) func(http.Handler) http.Handler` middleware that answers `403 forbidden` when the session user lacks `p`.

Roles and permissions:

| Permission      | admin | developer |
| --------------- | ----- | --------- |
| `user:manage`   | ✅    | ❌        |
| `shem:manage`   | ✅    | ❌        |
| `shem:view`     | ✅    | ✅        |
| `ticket:create` | ✅    | ✅        |
| `ticket:view`   | ✅    | ✅        |
| `ticket:manage` | ✅    | ✅        |

`shem:manage` has no HTTP route yet (shems are registered via the CLI and API key); it exists so the admin/developer split is expressed in the table rather than implied.

Rules to keep:

- **Adding a role or permission is one entry in `rolePermissions`** — never a new branch in a handler.
- **Deny by default.** A role absent from the table (including `""` and hand-edited values) holds nothing; a session route with no `rbac.Require` is a bug.
- **Permissions, not role checks, at the call site.** Nothing outside this package compares `user.Role` to a literal, except the last-admin guards in `orchestrator-admin` and `db.ensureAdmin` (which cannot import `rbac`).
- `Require` must be nested *inside* `auth.RequireSession`, which is what puts the user in the request context.

Why homebrewed rather than Casbin: the whole policy is 2 roles × 6 permissions, and the things an engine buys — a policy DSL, runtime-mutable policy, storage adapters — are all out of scope. The repo has no auth/authz dependencies, and the shape adopted here is exactly what an RBAC model in Casbin would encode, so `Can` is the single seam to replace with an enforcer if policy ever needs to be runtime-editable; no handler would change.
