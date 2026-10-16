package db

import (
	"path/filepath"
	"testing"
	"time"
)

// Re-opening an existing database must be non-destructive: AutoMigrate
// adds the lease table (and any future tables) without touching rows an
// older golem wrote.
func TestOpenPreservesExistingRowsAcrossMigration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "golem.db")

	first, err := Open(path)
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	ticket := Ticket{
		ID: "t-legacy", RepoRemote: "https://github.com/org/repo", BaseBranch: "main",
		Title: "legacy ticket", Branch: "ticket/legacy", Phase: "ready-for-review",
		Description: "written by an older golem", BodyHash: "abc",
	}
	if err := first.Create(&ticket).Error; err != nil {
		t.Fatalf("seed ticket: %v", err)
	}
	row := GitHubOutbox{
		TicketID: "t-legacy", Kind: "comment", Payload: `{"body":"hi"}`,
		IdempotencyKey: "k1", NextAttempt: time.Now(),
	}
	if err := first.Create(&row).Error; err != nil {
		t.Fatalf("seed outbox: %v", err)
	}
	sqlDB, _ := first.DB()
	_ = sqlDB.Close()

	second, err := Open(path)
	if err != nil {
		t.Fatalf("second Open (migration): %v", err)
	}
	t.Cleanup(func() { s, _ := second.DB(); _ = s.Close() })
	var got Ticket
	if err := second.Where("id = ?", "t-legacy").First(&got).Error; err != nil {
		t.Fatalf("legacy ticket lost across migration: %v", err)
	}
	if got.Phase != "ready-for-review" {
		t.Errorf("legacy ticket phase = %q, want ready-for-review", got.Phase)
	}
	var count int64
	if err := second.Model(&GitHubOutbox{}).Count(&count).Error; err != nil || count != 1 {
		t.Errorf("outbox rows across migration: count=%d err=%v, want 1", count, err)
	}
	// And the new table is usable.
	if _, acquired, err := AcquireLease(second, "ghsync-outbox-drain", "test", time.Minute); err != nil || !acquired {
		t.Errorf("lease table unusable after migration: acquired=%t err=%v", acquired, err)
	}
}

// TestIntegrationAttemptMigrationIsAdditive covers the M3 table: a database
// from before the lane existed gains it on Open without touching existing
// rows, and the per-head dedupe index rejects a second attempt for the same
// (epic, node, head).
func TestIntegrationAttemptMigrationIsAdditive(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pre-lane.db")
	first, err := Open(path)
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	first.Create(&Ticket{ID: "t-old", RepoRemote: "r", Phase: "closed"})
	sqlDB, _ := first.DB()
	_ = sqlDB.Close()

	second, err := Open(path)
	if err != nil {
		t.Fatalf("second Open: %v", err)
	}
	t.Cleanup(func() { s, _ := second.DB(); _ = s.Close() })
	a := IntegrationAttempt{EpicID: "e1", EpicNodeKey: "T1", TicketID: "t-old",
		PRNumber: 7, HeadSHA: "h1", State: "merged", MergeSHA: "m1"}
	if err := second.Create(&a).Error; err != nil {
		t.Fatalf("attempt insert: %v", err)
	}
	dup := a
	dup.ID = 0
	if err := second.Create(&dup).Error; err == nil {
		t.Fatal("same (epic, node, head) must be one attempt")
	}
	var got Ticket
	if err := second.Where("id = ?", "t-old").First(&got).Error; err != nil || got.Phase != "closed" {
		t.Errorf("existing row disturbed: phase=%q err=%v", got.Phase, err)
	}
}
