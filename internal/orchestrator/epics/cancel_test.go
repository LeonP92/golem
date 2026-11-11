package epics

import (
	"errors"
	"testing"
	"time"

	"gorm.io/gorm"

	"github.com/leonp92/golem/internal/orchestrator/db"
)

// closeKids is the hook the API passes: it closes each child it is handed.
func closeKids(seen *[]string) func(*gorm.DB, db.Ticket) error {
	return func(tx *gorm.DB, child db.Ticket) error {
		*seen = append(*seen, child.EpicNodeKey)
		return tx.Model(&db.Ticket{}).Where("id = ?", child.ID).Update("phase", "closed").Error
	}
}

// Cancelling a running run records who, why and when, hands every open child
// to the close hook, skips children already integrated, and frees the merge
// slot. Afterwards no lifecycle step accepts the run.
func TestCancelRunningEpic(t *testing.T) {
	e, gdb := runningEpic(t)
	if err := RecordIntegration(gdb, e.ID, "T1", "sha1"); err != nil {
		t.Fatal(err)
	}
	gdb.Create(&db.EpicIntegrationSlot{EpicID: e.ID, NodeKey: "T2", HeadSHA: "h", ShemID: 1, LeaseID: "l", ExpiresAt: time.Now().Add(time.Minute)})

	var seen []string
	if err := Cancel(gdb, e.ID, "wrong approach", "leon", closeKids(&seen)); err != nil {
		t.Fatal(err)
	}
	if len(seen) != 1 || seen[0] != "T2" {
		t.Fatalf("closed %v, want only the open child T2", seen)
	}
	var got db.Epic
	gdb.First(&got, "id = ?", e.ID)
	if got.State != StateCancelled || got.CancelReason != "wrong approach" || got.CancelledBy != "leon" || got.CancelledAt == nil {
		t.Fatalf("epic after cancel: state=%q reason=%q by=%q at=%v", got.State, got.CancelReason, got.CancelledBy, got.CancelledAt)
	}
	var slots int64
	gdb.Model(&db.EpicIntegrationSlot{}).Where("epic_id = ?", e.ID).Count(&slots)
	if slots != 0 {
		t.Fatal("cancel must free the integration slot")
	}
	assertChildPhase(t, gdb, e.ID, "T2", "closed")

	if _, err := Start(gdb, e.ID); !errors.Is(err, ErrWrongState) {
		t.Errorf("start after cancel: %v", err)
	}
	if err := Resume(gdb, e.ID); !errors.Is(err, ErrWrongState) {
		t.Errorf("resume after cancel: %v", err)
	}
	if err := Pause(gdb, e.ID); !errors.Is(err, ErrWrongState) {
		t.Errorf("pause after cancel: %v", err)
	}
	if _, err := AddRevision(gdb, e.ID, validPlan(), ""); !errors.Is(err, ErrNotEditable) {
		t.Errorf("revision after cancel: %v", err)
	}
	if _, err := GateIntegration(gdb, e.ID, "T2", "h", 1, time.Now()); !errors.Is(err, ErrWrongState) {
		t.Errorf("gate after cancel: %v", err)
	}
	if err := RecordIntegration(gdb, e.ID, "T2", "sha2"); !errors.Is(err, ErrWrongState) {
		t.Errorf("receipt after cancel: %v", err)
	}
	if err := CompleteFinal(gdb, e.ID, "final"); !errors.Is(err, ErrWrongState) {
		t.Errorf("merge-final after cancel: %v", err)
	}
}

// A repeated cancel succeeds without touching anything: the first reason,
// actor and time stand, and no child is handed to the hook again.
func TestCancelIsIdempotent(t *testing.T) {
	e, gdb := runningEpic(t)
	var seen []string
	if err := Cancel(gdb, e.ID, "first", "leon", closeKids(&seen)); err != nil {
		t.Fatal(err)
	}
	seen = nil
	if err := Cancel(gdb, e.ID, "second", "other", closeKids(&seen)); err != nil {
		t.Fatalf("second cancel: %v", err)
	}
	var got db.Epic
	gdb.First(&got, "id = ?", e.ID)
	if len(seen) != 0 || got.CancelReason != "first" || got.CancelledBy != "leon" {
		t.Fatalf("second cancel changed things: seen=%v reason=%q by=%q", seen, got.CancelReason, got.CancelledBy)
	}
}

// Completed runs are delivered and stay that way; unknown runs are 404s.
func TestCancelRefusesCompletedAndMissing(t *testing.T) {
	gdb, _ := db.Open(":memory:")
	e := createEpic(t, gdb)
	gdb.Model(&db.Epic{}).Where("id = ?", e.ID).Update("state", StateCompleted)
	var seen []string
	if err := Cancel(gdb, e.ID, "", "leon", closeKids(&seen)); !errors.Is(err, ErrWrongState) {
		t.Fatalf("cancel completed: %v", err)
	}
	if err := Cancel(gdb, "nope", "", "leon", closeKids(&seen)); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cancel missing: %v", err)
	}
}

// Every pre-completion state cancels.
func TestCancelFromEveryOpenState(t *testing.T) {
	for _, s := range []string{StateDraft, StateAwaitingApproval, StateRunning, StatePaused, StateAwaitingFinalReview} {
		gdb, _ := db.Open(":memory:")
		e := createEpic(t, gdb)
		gdb.Model(&db.Epic{}).Where("id = ?", e.ID).Update("state", s)
		var seen []string
		if err := Cancel(gdb, e.ID, "", "leon", closeKids(&seen)); err != nil {
			t.Errorf("cancel from %s: %v", s, err)
		}
	}
}

// A plan job still queued or running is failed with the cancel, so no shem
// picks it up and a running one cannot land a draft afterwards.
func TestCancelFailsActivePlanJob(t *testing.T) {
	gdb, _ := db.Open(":memory:")
	e := createEpic(t, gdb)
	_, job, err := RequestPlanJob(gdb, e.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ClaimPlanJob(gdb, job.ID, 7, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	var seen []string
	if err := Cancel(gdb, e.ID, "", "leon", closeKids(&seen)); err != nil {
		t.Fatal(err)
	}
	var got db.EpicPlanJob
	gdb.First(&got, "id = ?", job.ID)
	if got.Status != PlanJobFailed {
		t.Fatalf("plan job status = %q, want failed", got.Status)
	}
	if err := CompletePlanJob(gdb, job.ID, 7, "{}", ""); !errors.Is(err, ErrPlanJobNotOwned) {
		t.Fatalf("complete after cancel: %v", err)
	}
	if _, _, err := RequestPlanJob(gdb, e.ID); !errors.Is(err, ErrNotEditable) {
		t.Fatalf("new plan job after cancel: %v", err)
	}
}

// A hook failure rolls the whole cancel back: the run is not left cancelled
// with children still open.
func TestCancelRollsBackOnHookError(t *testing.T) {
	e, gdb := runningEpic(t)
	boom := errors.New("boom")
	err := Cancel(gdb, e.ID, "", "leon", func(*gorm.DB, db.Ticket) error { return boom })
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
	var got db.Epic
	gdb.First(&got, "id = ?", e.ID)
	if got.State != StateRunning {
		t.Fatalf("state = %q after a failed cancel", got.State)
	}
}
