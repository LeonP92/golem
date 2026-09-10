# internal/orchestrator/admin

The admin module provides CLI-callable helper functions for managing orchestrator users and shems (agent API keys). It handles interactive and non-interactive password collection (falling back to the GOLEM_ADMIN_PASSWORD env var), bcrypt hashing, and CRUD operations against the database via GORM — covering creation, upsert, and hard-deletion of both User and Shem records. It exists as the administrative control plane for the orchestrator's credential store, used during initial setup, Docker bootstrap, and day-to-day user management.

## Functions

- UsersAdd
- UsersRemove
- ShemsAdd
- ShemsAddOrUpdate
- UsersAddOrUpdate
- ShemsRemove

## Imports

crypto/rand, encoding/hex, errors, fmt, os, golang.org/x/crypto/bcrypt, golang.org/x/term, github.com/leonp92/golem/internal/orchestrator/db, gorm.io/gorm
