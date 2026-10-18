package worker

import (
	"testing"
	"time"

	"github.com/leonp92/golem/internal/orchestrator/db"
	"github.com/leonp92/golem/internal/shem/client"
)

// Exercise the real HTTP boundary: ownership alone cannot authorize a receipt.
func TestIntegrationReceiptRequiresTestedLease(t *testing.T) {
	remote := "https://example.com/org/repo"
	s := newLaneStack(t, remote)
	id := s.startLaneEpic(remote, []map[string]any{laneNode("A", "one")}, nil)
	var child db.Ticket
	if err := s.gdb.First(&child, "epic_id = ?", id).Error; err != nil {
		t.Fatal(err)
	}
	var sh db.Shem
	s.gdb.First(&sh)
	if err := s.gdb.Model(&child).Updates(map[string]any{"assigned_shem": sh.ID, "phase": "ready-for-review"}).Error; err != nil {
		t.Fatal(err)
	}
	result := client.IntegrationResult{Status: "merged", MergeSHA: "merge", HeadSHA: "head"}
	if err := s.shem.PostIntegrationResult(id, "A", result); err == nil {
		t.Fatal("ungranted receipt accepted")
	}
	if _, err := s.shem.CheckIntegrationGate(id, "A", "head"); err == nil {
		t.Fatal("gate granted without tester pass")
	}
	if err := s.gdb.Create(&db.TesterVerdict{EpicID: id, EpicNodeKey: "A", HeadSHA: "head", Verdict: "pass"}).Error; err != nil {
		t.Fatal(err)
	}
	gate, err := s.shem.CheckIntegrationGate(id, "A", "head")
	if err != nil {
		t.Fatal(err)
	}
	result.LeaseID = gate.LeaseID
	// Reassignment revokes the old holder, even with a previously valid lease.
	other := db.Shem{Name: "replacement", APIKeyHash: "fixture"}
	if err := s.gdb.Create(&other).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.gdb.Model(&child).Update("assigned_shem", other.ID).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.shem.PostIntegrationResult(id, "A", result); err == nil {
		t.Fatal("former owner reported a receipt")
	}
	if err := s.gdb.Model(&child).Update("assigned_shem", sh.ID).Error; err != nil {
		t.Fatal(err)
	}
	for _, invalid := range []client.IntegrationResult{
		{Status: "merged", MergeSHA: "merge", HeadSHA: "head", LeaseID: "old-lease"},
		{Status: "error", HeadSHA: "other-head", LeaseID: gate.LeaseID, Detail: "stale failure"},
	} {
		if err := s.shem.PostIntegrationResult(id, "A", invalid); err == nil {
			t.Fatalf("invalid receipt accepted: %+v", invalid)
		}
	}
	if got := laneTicketPhase(t, s.gdb, child.ID); got != "ready-for-review" {
		t.Fatalf("stale failure changed phase: %s", got)
	}
	// A newer tester failure revokes the head's authority even with a lease.
	if err := s.gdb.Create(&db.TesterVerdict{EpicID: id, EpicNodeKey: "A", HeadSHA: "head", Verdict: "fail"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.shem.PostIntegrationResult(id, "A", result); err == nil {
		t.Fatal("receipt accepted after tester revocation")
	}
	if err := s.gdb.Create(&db.TesterVerdict{EpicID: id, EpicNodeKey: "A", HeadSHA: "head", Verdict: "pass"}).Error; err != nil {
		t.Fatal(err)
	}
	// A result cannot overwrite a human stop.
	s.gdb.Model(&child).Update("phase", "closed")
	if err := s.shem.PostIntegrationResult(id, "A", result); err == nil {
		t.Fatal("closed child integrated")
	}
	s.gdb.Model(&child).Update("phase", "ready-for-review")
	// Expired authority must be reacquired before reporting the contained head.
	s.gdb.Model(&db.EpicIntegrationSlot{}).Where("epic_id = ?", id).Update("expires_at", time.Now().Add(-time.Second))
	if err := s.shem.PostIntegrationResult(id, "A", result); err == nil {
		t.Fatal("expired lease accepted")
	}
	gate, err = s.shem.CheckIntegrationGate(id, "A", "head")
	if err != nil {
		t.Fatal(err)
	}
	result.LeaseID = gate.LeaseID
	if err := s.shem.PostIntegrationResult(id, "A", result); err != nil {
		t.Fatal(err)
	}
	if err := s.shem.PostIntegrationResult(id, "A", result); err != nil {
		t.Fatalf("identical receipt not idempotent: %v", err)
	}
	if got := laneEpicState(t, s.gdb, id); got != "awaiting-final-review" {
		t.Fatalf("state=%s", got)
	}
}
