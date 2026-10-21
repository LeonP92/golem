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
// the migration and the admin bootstrap against existing rows. Registers a
// Cleanup that closes the underlying *sql.DB — otherwise Windows keeps the file
// locked and t.TempDir's RemoveAll fails.
func openFile(t *testing.T, dir string) *gorm.DB {
	t.Helper()
	gdb, err := db.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() {
		if sqlDB, err := gdb.DB(); err == nil {
			sqlDB.Close()
		}
	})
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

// A database from before the decision columns upgrades to autonomous: the
// column default fills every existing ticket and epic.
func TestOpen_BackfillsDecisionsAsAutonomous(t *testing.T) {
	dir := t.TempDir()
	gdb := openFile(t, dir)
	if err := gdb.Create(&db.Ticket{ID: "t1", RepoRemote: "r", Title: "t", Description: "d", Phase: "unassigned"}).Error; err != nil {
		t.Fatalf("create ticket: %v", err)
	}
	if err := gdb.Create(&db.Epic{ID: "e1", RepoRemote: "r", Title: "e", SpecMarkdown: "s", SpecHash: "h"}).Error; err != nil {
		t.Fatalf("create epic: %v", err)
	}
	for _, table := range []string{"tickets", "epics"} {
		for _, col := range []string{"ask_decisions", "decision_guidance"} {
			if err := gdb.Exec("ALTER TABLE " + table + " DROP COLUMN " + col).Error; err != nil {
				t.Fatalf("drop %s.%s: %v", table, col, err)
			}
		}
	}

	gdb = openFile(t, dir)
	var tk db.Ticket
	var ep db.Epic
	if err := gdb.First(&tk, "id = ?", "t1").Error; err != nil {
		t.Fatal(err)
	}
	if err := gdb.First(&ep, "id = ?", "e1").Error; err != nil {
		t.Fatal(err)
	}
	if tk.AskDecisions || ep.AskDecisions || tk.DecisionGuidance != "" || ep.DecisionGuidance != "" {
		t.Fatalf("backfill: ticket %v/%q epic %v/%q", tk.AskDecisions, tk.DecisionGuidance, ep.AskDecisions, ep.DecisionGuidance)
	}
}
