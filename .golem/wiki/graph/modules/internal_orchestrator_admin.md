# internal/orchestrator/admin

The admin module provides the CLI- and bootstrap-callable control plane for the orchestrator's credential store, managing both human users and shems (agent nodes with API keys) directly against the database via GORM. For users it validates role strings through the rbac package, collects passwords interactively via terminal prompt or non-interactively from the GOLEM_ADMIN_PASSWORD environment variable for Docker setups, bcrypt-hashes them, and supports create, list, role change, upsert, and hard-delete operations. Deletion and demotion are guarded by a last-admin check that refuses any operation which would leave the orchestrator with zero admins, and removing a user also deletes their sessions so access is revoked immediately rather than lingering until token expiry. For shems it generates random 32-byte hex API keys printed once to stdout, stores only their bcrypt hashes, and supports upsert and removal for env-var-driven auto-provisioning at server startup.

## Functions

- UsersAdd
- UsersList
- UsersSetRole
- UsersRemove
- ShemsAdd
- ShemsAddOrUpdate
- UsersAddOrUpdate
- ShemsRemove
- TestUsersAdd_RejectsUnknownRole
- TestUsersAddOrUpdate_UpdatesRoleOnExistingUser
- TestUsersList_OrderedByUsername
- TestUsersSetRole_RefusesDemotingLastAdmin
- TestUsersSetRole_AllowsDemotionWhenAnotherAdminExists
- TestUsersSetRole_RejectsUnknownRole
- TestUsersRemove_RefusesRemovingLastAdmin
- TestUsersRemove_DeletesSessions
- TestUsersRemove_UnknownUser

## Imports

crypto/rand, encoding/hex, errors, fmt, os, strings, golang.org/x/crypto/bcrypt, golang.org/x/term, github.com/leonp92/golem/internal/orchestrator/db, github.com/leonp92/golem/internal/orchestrator/rbac, gorm.io/gorm, net/http/httptest, testing, github.com/leonp92/golem/internal/orchestrator/admin, github.com/leonp92/golem/internal/orchestrator/auth
