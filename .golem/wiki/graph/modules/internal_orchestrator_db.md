# internal/orchestrator/db

This module provides database connectivity and schema management for the orchestrator server. It exposes a single Open function that selects between PostgreSQL and SQLite drivers based on the DSN prefix, runs GORM AutoMigrate to create or update all model tables, and enables WAL journal mode for SQLite to allow concurrent access. The models it defines — User, Session, Shem, Ticket, LogEntry, and HumanInput — represent the full persistent state of the orchestrator: web UI users and their sessions, registered Shem worker agents, work tickets and their lifecycle phase, structured log entries produced by Shems, and human-input requests that block ticket progress pending operator response.

## Functions

- Open
- TestOpenAndMigrate
- BeforeCreate

## Types

- User
- Session
- Shem
- Ticket
- LogEntry
- HumanInput

## Imports

strings, github.com/glebarez/sqlite, gorm.io/driver/postgres, gorm.io/gorm, testing, github.com/leonp92/golem/internal/orchestrator/db, time, github.com/google/uuid
