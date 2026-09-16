# internal/orchestrator/db

This module is the persistence layer for the Golem orchestrator server. It exposes a single Open function that picks a PostgreSQL or pure-Go SQLite driver based on the DSN prefix, enables SQLite WAL journal mode so the server and admin CLI can share one database file concurrently, runs GORM AutoMigrate over every model, and then bootstraps an admin: if no user carries the "admin" role it promotes the lowest-ID existing user, which keeps a pre-RBAC deployment (whose rows AutoMigrate backfills with the default "developer" role) from locking itself out of user management and also recovers a database whose last admin was deleted out-of-band. The role name is written as a string literal rather than imported because the rbac package depends on db, not the reverse. The models it defines — User (with an rbac role name), Session, Shem, Ticket, LogEntry, and HumanInput — represent the full persistent state of the orchestrator: web UI users and their sessions, registered Shem workers and the repos they manage, work tickets with their lifecycle phase, checkpoint and creating user, structured log entries produced by Shems, and human-input requests that block ticket progress pending an operator response. It also provides small query helpers on those models, including a batched creator-name lookup for ticket lists and JSON decoding of a Shem's repo list.

## Functions

- Open
- TestOpenAndMigrate
- TestOpen_DefaultsRoleToDeveloper
- TestOpen_PromotesFirstUserWhenNoAdmin
- TestOpen_NoopWhenAdminExists
- TestOpen_EmptyUsersTable
- RepoList
- BeforeCreate
- CreatorNames

## Types

- User
- Session
- Shem
- Ticket
- LogEntry
- HumanInput

## Imports

strings, github.com/glebarez/sqlite, gorm.io/driver/postgres, gorm.io/gorm, path/filepath, testing, github.com/leonp92/golem/internal/orchestrator/db, encoding/json, time, github.com/google/uuid
