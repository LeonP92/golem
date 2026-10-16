Package `internal/orchestrator/db` defines GORM model structs for all six orchestrator database tables (User, Session, Shem, Ticket, LogEntry, HumanInput) and provides an `Open(dsn string) (*gorm.DB, error)` function that selects the pure-Go SQLite driver (`github.com/glebarez/sqlite`) for local paths or the PostgreSQL driver (`gorm.io/driver/postgres`) for `postgres://`/`postgresql://` DSNs, then calls `AutoMigrate` on all six models. Use this package as the single entry point for database setup; every other orchestrator package receives a `*gorm.DB` from here.

`Ticket` also has a required `Title` (human-entered, used by the API/UI to compute the branch name via `internal/slug`) and `BaseBranch` (the branch it forked from, distinct from `Branch`, the computed working branch name).

`User` has a `Role` string column (`not null;default:'developer'`) holding an rbac role name — see `orchestrator-rbac`. Because the default backfills pre-existing rows as `developer`, `Open` runs an idempotent bootstrap (`ensureAdmin`) after `AutoMigrate`: if the users table is non-empty and no row has `role = 'admin'`, the lowest-ID user is promoted to `admin`. That keeps an upgraded deployment (and one whose last admin was deleted out-of-band) from being locked out of user management. The role name is a literal there because `db` must not import `rbac` — `rbac` imports `db`.

`Ticket` also tracks `CreatedByUserID *uint` (nullable, indexed, references `User.ID`), set from the session user at creation time by the API and UI handlers. `CreatorNames(gdb *gorm.DB, tickets []Ticket) map[uint]string` resolves a batch of tickets' `CreatedByUserID` to usernames in a single query, for use by list/detail endpoints and templates that need to render "created by X" without an N+1 lookup.

## Model selection

`Ticket.Models` (JSON selections) and `Ticket.ModelBackend` (the backend its
ids were validated against; empty means any) join `Shem.Backend` and
`Shem.Catalog` (the reported catalog, as JSON). `Ticket.ModelSelections` /
`SetModelSelections` and `Shem.ModelCatalog` decode them, returning an empty
value on malformed JSON.

`fleet.go` — `Fleet` is every registered shem's reported catalog, loaded once
by `LoadFleet` (one query; offline rows count, since a catalog describes a
machine rather than its liveness). `Backends` returns each backend's union
catalog as a `BackendCatalog`, `Catalog` one by name, `Catalogs` all of them
keyed by name, and `WaitingFor` why no shem can claim a ticket.

## Leases

`Lease` (`lease.go`) is a named, expiring lock with a monotonic fencing
token: `AcquireLease` (grants only when free, expired, or re-acquired by the
same holder — always bumping the token), `RenewLease`, `CheckFencing` (is
this token still current — the write-side guard against stale holders) and
`ReleaseLease` (holder+token checked; expires the row rather than deleting
it, so tokens never reset). The ghsync drain loop holds the
`ghsync-outbox-drain` lease while draining, enforcing the outbox's
single-drainer assumption; the epic integration lane holds one lease per
repo + target ref and checks its token before the merge call.

Epic child columns on `Ticket`: `EpicID`/`EpicNodeKey`/`EpicPlanRevision`
(unique together — one materialized ticket per plan node) and `EpicReady`
(the orchestration-readiness flag, deliberately separate from `Phase`).
All nullable/defaulted, so pre-epic databases migrate forward with every
existing ticket reading as ordinary work. `EpicNode` rows carry the
integration receipt (`IntegratedSHA`/`IntegratedRef`/`IntegratedAt`) and
the node -> ticket mapping.

### IntegrationAttempt
One row per integration-lane merge attempt against a child pull request: written before the GitHub call and resolved after (a crash leaves a `merging` row the lane reconciles from the remote). Unique on (epic, node, head SHA); the final merge uses an empty node key. Failed states carry a named `cause`.

`TesterVerdict` rows are append-only verdicts (epic, node key, head SHA, verdict, scenarios, evidence).
