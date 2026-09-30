package db_test

import (
	"testing"

	"gorm.io/gorm"

	"github.com/leonp92/golem/internal/orchestrator/db"
)

func seedReaped(t *testing.T, gdb *gorm.DB) db.Ticket {
	t.Helper()
	shemID := uint(7)
	tk := db.Ticket{RepoRemote: "r", Branch: "b", Description: "d", Phase: "unassigned",
		ReapedFromShem: &shemID, ReapedFromPhase: "implement"}
	if err := gdb.Create(&tk).Error; err != nil {
		t.Fatalf("create: %v", err)
	}
	return tk
}

// A reaped record is only valid while the ticket sits in the pool untouched.
// Any other phase change — the issue-body re-gate to pending-approval, a
// human stop — must end it, or a later re-approval or resume would let the
// shem still running the OLD work take the ticket back behind that decision.
// Clearing it at each such call site was tried in review and already missed
// two of them, so it is cleared by the model on every phase write.
func TestTicket_PhaseChangeClearsTheReapedRecord(t *testing.T) {
	gdb, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	for _, phase := range []string{"pending-approval", "stopped", "closed", "unassigned"} {
		tk := seedReaped(t, gdb)
		if err := gdb.Model(&db.Ticket{}).Where("id = ?", tk.ID).
			Updates(map[string]any{"phase": phase}).Error; err != nil {
			t.Fatalf("update: %v", err)
		}
		var got db.Ticket
		gdb.First(&got, "id = ?", tk.ID)
		if got.ReapedFromShem != nil || got.ReapedFromPhase != "" {
			t.Errorf("phase -> %s left reaped record %v %q", phase, got.ReapedFromShem, got.ReapedFromPhase)
		}
	}
}

// Writes that do not touch phase (the issue sync refreshing title and body)
// and the reaper's own write, which sets the record, must leave it intact.
func TestTicket_ReapedRecordSurvivesOtherWrites(t *testing.T) {
	gdb, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	tk := seedReaped(t, gdb)
	if err := gdb.Model(&db.Ticket{}).Where("id = ?", tk.ID).
		Updates(map[string]any{"title": "new title", "body_hash": "h"}).Error; err != nil {
		t.Fatalf("update: %v", err)
	}
	var got db.Ticket
	gdb.First(&got, "id = ?", tk.ID)
	if got.ReapedFromShem == nil || got.ReapedFromPhase != "implement" {
		t.Errorf("non-phase write cleared reaped record: %v %q", got.ReapedFromShem, got.ReapedFromPhase)
	}

	other := uint(9)
	if err := gdb.Model(&db.Ticket{}).Where("id = ?", tk.ID).Updates(map[string]any{
		"phase": "unassigned", "reaped_from_shem": other, "reaped_from_phase": "plan",
	}).Error; err != nil {
		t.Fatalf("update: %v", err)
	}
	gdb.First(&got, "id = ?", tk.ID)
	if got.ReapedFromShem == nil || *got.ReapedFromShem != other || got.ReapedFromPhase != "plan" {
		t.Errorf("reaper-style write lost its record: %v %q", got.ReapedFromShem, got.ReapedFromPhase)
	}
}
