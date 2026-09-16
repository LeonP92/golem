package db_test

import (
	"path/filepath"
	"testing"

	"gorm.io/gorm"

	"github.com/leonp92/golem/internal/orchestrator/db"
)

func TestOpenAndMigrate(t *testing.T) {
	gdb, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	// Verify all tables exist by creating and querying a record
	user := db.User{Username: "alice", PasswordHash: "hash"}
	if err := gdb.Create(&user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	var found db.User
	if err := gdb.First(&found, "username = ?", "alice").Error; err != nil {
		t.Fatalf("find user: %v", err)
	}
	if found.Username != "alice" {
		t.Errorf("got %q, want alice", found.Username)
	}
}

// openFile opens a file-backed database in a temp dir so a second Open re-runs
// the migration and the admin bootstrap against existing rows.
func openFile(t *testing.T, dir string) *gorm.DB {
	t.Helper()
	gdb, err := db.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return gdb
}

func roleOf(t *testing.T, gdb *gorm.DB, username string) string {
	t.Helper()
	var u db.User
	if err := gdb.Where("username = ?", username).First(&u).Error; err != nil {
		t.Fatalf("find %s: %v", username, err)
	}
	return u.Role
}

func TestOpen_DefaultsRoleToDeveloper(t *testing.T) {
	gdb := openFile(t, t.TempDir())
	if err := gdb.Create(&db.User{Username: "alice", PasswordHash: "x"}).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	if got := roleOf(t, gdb, "alice"); got != "developer" {
		t.Errorf("got role %q, want developer", got)
	}
}

func TestOpen_PromotesFirstUserWhenNoAdmin(t *testing.T) {
	dir := t.TempDir()
	gdb := openFile(t, dir)
	for _, name := range []string{"alice", "bob"} {
		if err := gdb.Create(&db.User{Username: name, PasswordHash: "x", Role: "developer"}).Error; err != nil {
			t.Fatalf("create %s: %v", name, err)
		}
	}
	// Simulate a pre-RBAC database: nobody is an admin.
	if err := gdb.Model(&db.User{}).Where("1 = 1").Update("role", "developer").Error; err != nil {
		t.Fatalf("clear roles: %v", err)
	}

	gdb = openFile(t, dir)
	if got := roleOf(t, gdb, "alice"); got != "admin" {
		t.Errorf("lowest-ID user: got role %q, want admin", got)
	}
	if got := roleOf(t, gdb, "bob"); got != "developer" {
		t.Errorf("bob: got role %q, want developer", got)
	}
}

func TestOpen_NoopWhenAdminExists(t *testing.T) {
	dir := t.TempDir()
	gdb := openFile(t, dir)
	if err := gdb.Create(&db.User{Username: "alice", PasswordHash: "x", Role: "developer"}).Error; err != nil {
		t.Fatalf("create alice: %v", err)
	}
	if err := gdb.Create(&db.User{Username: "bob", PasswordHash: "x", Role: "admin"}).Error; err != nil {
		t.Fatalf("create bob: %v", err)
	}

	gdb = openFile(t, dir)
	if got := roleOf(t, gdb, "alice"); got != "developer" {
		t.Errorf("alice: got role %q, want developer (bootstrap should be a no-op)", got)
	}
}

func TestOpen_EmptyUsersTable(t *testing.T) {
	gdb := openFile(t, t.TempDir())
	var count int64
	if err := gdb.Model(&db.User{}).Count(&count).Error; err != nil {
		t.Fatalf("count users: %v", err)
	}
	if count != 0 {
		t.Errorf("got %d users, want 0", count)
	}
}
