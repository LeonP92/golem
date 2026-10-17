package epics

import (
	"errors"
	"testing"

	"gorm.io/gorm"

	"github.com/leonp92/golem/internal/orchestrator/db"
)

func TestCompleteFinal(t *testing.T) {
	gdb, _ := db.Open(":memory:")
	e := createEpic(t, gdb)
	rev, err := AddRevision(gdb, e.ID, validPlan(), "")
	if err != nil {
		t.Fatalf("revision: %v", err)
	}
	if err := Approve(gdb, e.ID, rev.Revision, rev.PlanHash, "leon"); err != nil {
		t.Fatalf("approve: %v", err)
	}
	if _, err := Start(gdb, e.ID); err != nil {
		t.Fatalf("start: %v", err)
	}

	// The attestation needs a run that is awaiting it, and a SHA.
	if err := CompleteFinal(gdb, e.ID, "base-sha"); !errors.Is(err, ErrWrongState) {
		t.Fatalf("attestation while running = %v", err)
	}
	if err := CompleteFinal(gdb, e.ID, ""); err == nil {
		t.Fatal("empty SHA accepted")
	}
	if err := RecordIntegration(gdb, e.ID, "T1", "m1"); err != nil {
		t.Fatalf("receipt T1: %v", err)
	}
	if err := RecordIntegration(gdb, e.ID, "T2", "m2"); err != nil {
		t.Fatalf("receipt T2: %v", err)
	}
	if err := CompleteFinal(gdb, e.ID, "base-sha"); err != nil {
		t.Fatalf("attestation: %v", err)
	}
	var fresh db.Epic
	gdb.First(&fresh, "id = ?", e.ID)
	if fresh.State != StateCompleted || fresh.FinalSHA != "base-sha" {
		t.Fatalf("after attestation: %+v", fresh)
	}
	// Repeating the same attestation is a no-op; a different one conflicts.
	if err := CompleteFinal(gdb, e.ID, "base-sha"); err != nil {
		t.Fatalf("repeat: %v", err)
	}
	if err := CompleteFinal(gdb, e.ID, "other-sha"); !errors.Is(err, ErrWrongState) {
		t.Fatalf("conflicting repeat = %v", err)
	}
}

// The transition to awaiting-final-review runs the emit hooks inside the
// receipt's transaction; a run with no hooks behaves identically.
func TestRecordIntegrationEmitHooks(t *testing.T) {
	gdb, _ := db.Open(":memory:")
	e := createEpic(t, gdb)
	rev, err := AddRevision(gdb, e.ID, validPlan(), "")
	if err != nil {
		t.Fatalf("revision: %v", err)
	}
	if err := Approve(gdb, e.ID, rev.Revision, rev.PlanHash, "leon"); err != nil {
		t.Fatalf("approve: %v", err)
	}
	if _, err := Start(gdb, e.ID); err != nil {
		t.Fatalf("start: %v", err)
	}
	var fired []string
	hook := func(tx *gorm.DB, e *db.Epic) error {
		fired = append(fired, e.ID)
		return nil
	}
	// Mid-run receipts do not fire; the completing one does, once.
	if err := RecordIntegration(gdb, e.ID, "T1", "m1", hook); err != nil {
		t.Fatalf("receipt T1: %v", err)
	}
	if len(fired) != 0 {
		t.Fatalf("hook fired mid-run: %v", fired)
	}
	if err := RecordIntegration(gdb, e.ID, "T2", "m2", hook); err != nil {
		t.Fatalf("receipt T2: %v", err)
	}
	if len(fired) != 1 || fired[0] != e.ID {
		t.Fatalf("hook = %v", fired)
	}
}
