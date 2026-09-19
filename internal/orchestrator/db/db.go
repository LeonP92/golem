package db

import (
	"fmt"
	"log"
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
	isPostgres := strings.HasPrefix(dsn, "postgres://") || strings.HasPrefix(dsn, "postgresql://")
	if isPostgres {
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
	// Skipped entirely on Postgres, which has no PRAGMA: running it there
	// made every boot log `ERROR: syntax error at or near "PRAGMA"` for a
	// statement that was never applicable, which is exactly the kind of
	// noise that trains an operator to ignore startup errors. On SQLite a
	// failure is still logged rather than fatal — WAL is an optimisation,
	// not a requirement.
	if !isPostgres {
		if err := gdb.Exec("PRAGMA journal_mode=WAL").Error; err != nil {
			log.Printf("db: enable WAL mode: %v", err)
		}
	}
	if err := gdb.AutoMigrate(
		&User{}, &Session{}, &Shem{}, &Ticket{}, &LogEntry{}, &HumanInput{},
		&GitHubRepo{}, &GitHubOutbox{},
	); err != nil {
		return nil, err
	}
	if err := ensureAdmin(gdb); err != nil {
		return nil, err
	}
	return gdb, nil
}

// ensureAdmin guarantees a non-empty users table always contains at least one
// admin. AutoMigrate backfills pre-existing rows with the default 'developer'
// role, which would otherwise lock an upgraded deployment out of user
// management; the same guard also recovers a database whose last admin was
// deleted out-of-band. Idempotent: a no-op once any admin exists.
//
// The role name is a literal here because db must not import
// internal/orchestrator/rbac — rbac imports db.
func ensureAdmin(gdb *gorm.DB) error {
	var admins int64
	if err := gdb.Model(&User{}).Where("role = ?", "admin").Count(&admins).Error; err != nil {
		return err
	}
	if admins > 0 {
		return nil
	}
	// Find rather than First: an empty users table is the normal first-run case,
	// and First would log a spurious "record not found" on every startup.
	var first []User
	if err := gdb.Order("id asc").Limit(1).Find(&first).Error; err != nil {
		return err
	}
	if len(first) == 0 {
		return nil // empty database: nothing to promote
	}
	if err := gdb.Model(&first[0]).Update("role", "admin").Error; err != nil {
		return err
	}
	// Log it: this is a privilege grant, and an operator who lost their last
	// admin out-of-band should be able to see where the new one came from.
	log.Printf("no admin user found; promoted %q (id %d) to admin", first[0].Username, first[0].ID)
	return nil
}
