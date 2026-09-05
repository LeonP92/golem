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
	return gdb, gdb.AutoMigrate(
		&User{}, &Session{}, &Shem{}, &Ticket{}, &LogEntry{}, &HumanInput{},
	)
}
