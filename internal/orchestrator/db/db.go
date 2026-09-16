package db

import (
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
	// Enable WAL mode so multiple processes (server + admin CLI) can access
	// the same database file concurrently without exclusive-lock conflicts.
	gdb.Exec("PRAGMA journal_mode=WAL")
	if err := gdb.AutoMigrate(
		&User{}, &Session{}, &Shem{}, &Ticket{}, &LogEntry{}, &HumanInput{},
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
	return gdb.Model(&first[0]).Update("role", "admin").Error
}
