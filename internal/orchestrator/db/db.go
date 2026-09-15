package db

import (
	"fmt"
	"strings"

	"github.com/glebarez/sqlite"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

// Open opens a database connection for the given DSN and runs AutoMigrate on
// all model structs. If the DSN starts with "postgres://" or "postgresql://",
// the PostgreSQL driver is used; otherwise the pure-Go SQLite driver is used.
func Open(dsn string) (*gorm.DB, error) {
	var dialector gorm.Dialector
	if strings.HasPrefix(dsn, "postgres://") || strings.HasPrefix(dsn, "postgresql://") {
		dialector = postgres.Open(dsn)
	} else {
		dialector = sqlite.Open(dsn)
	}
	gdb, err := gorm.Open(dialector, &gorm.Config{})
	if err != nil {
		return nil, err
	}
	if dsn == ":memory:" {
		// glebarez/sqlite's ":memory:" DSN has no shared cache: every new
		// pooled connection sees a brand-new, empty database rather than the
		// one AutoMigrate just populated below. That is invisible under
		// sequential access (the pool never grows past one connection), but
		// any genuinely concurrent caller — such as the ghsync worker's
		// ingest and drain loops running alongside the goroutine that opened
		// this handle — can have the pool open a second connection and hit
		// "no such table", deterministically reproducible under `go test
		// -race`. Pinning the pool to one connection keeps every query
		// against the same in-process database; harmless for tests and never
		// reached in production, which always opens a real file path or a
		// Postgres DSN.
		sqlDB, err := gdb.DB()
		if err != nil {
			return nil, fmt.Errorf("get sql.DB handle for in-memory database: %w", err)
		}
		sqlDB.SetMaxOpenConns(1)
		// The pin above only helps as long as that one connection is never
		// recycled: SetMaxIdleConns(1) stops it being closed as idle, and
		// SetConnMaxLifetime(0) (no limit) stops it being closed on age. Either
		// one being left at its default would eventually close the sole
		// connection to this ":memory:" database — and with it, silently and
		// confusingly, every table AutoMigrate just created.
		sqlDB.SetMaxIdleConns(1)
		sqlDB.SetConnMaxLifetime(0)
	}
	// Enable WAL mode so multiple processes (server + admin CLI) can access
	// the same database file concurrently without exclusive-lock conflicts.
	gdb.Exec("PRAGMA journal_mode=WAL")
	// This orchestrator now has its first pair of background writers — the
	// ghsync worker's ingest and drain loops — issuing writes against one
	// SQLite file alongside ordinary request handling. WAL allows concurrent
	// readers, but two writers can still collide on SQLITE_BUSY; a busy
	// timeout makes SQLite retry for a while internally instead of failing
	// the call immediately.
	gdb.Exec("PRAGMA busy_timeout=5000")
	return gdb, gdb.AutoMigrate(
		&User{}, &Session{}, &Shem{}, &Ticket{}, &LogEntry{}, &HumanInput{},
		&GitHubRepo{}, &GitHubOutbox{},
	)
}
