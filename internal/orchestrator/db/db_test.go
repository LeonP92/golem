package db_test

import (
	"testing"

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
