package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/leonpham/golem/internal/ticket"
)

func TestTicketAdvanceChangesPhase(t *testing.T) {
	repo := t.TempDir()
	ticketDir := filepath.Join(repo, ".golem", "tickets", "t1")
	s := ticket.New("t1", "", false)
	if err := s.Save(ticketDir); err != nil {
		t.Fatalf("Save: %v", err)
	}

	var stdout, stderr bytes.Buffer
	code := TicketAdvance([]string{"--repo", repo, "--ticket", "t1", "--to", "plan"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("TicketAdvance failed: exit %d, stderr=%s", code, stderr.String())
	}
	loaded, err := ticket.Load(ticketDir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded.Phase != ticket.PhasePlan {
		t.Errorf("Phase = %q, want plan", loaded.Phase)
	}
}

func TestTicketAdvanceRejectsUnknownPhase(t *testing.T) {
	repo := t.TempDir()
	ticketDir := filepath.Join(repo, ".golem", "tickets", "t1")
	ticket.New("t1", "", false).Save(ticketDir)

	var stdout, stderr bytes.Buffer
	code := TicketAdvance([]string{"--repo", repo, "--ticket", "t1", "--to", "not-a-real-phase"}, &stdout, &stderr)
	if code == 0 {
		t.Fatal("expected an error for an unknown phase")
	}
}

func TestTicketReviewRunsGateAndSetsReadyForReview(t *testing.T) {
	repo, _, ticketID := setUpTicketForStepTest(t)
	os.MkdirAll(filepath.Join(repo, ".golem", "roles"), 0o755)
	os.WriteFile(filepath.Join(repo, ".golem", "roles", "reviewer.md"), []byte("# reviewer\n"), 0o644)
	os.WriteFile(filepath.Join(repo, ".golem", "config.yaml"), []byte("backend: claude-code\ngate:\n  commands:\n    - \"true\"\n"), 0o644)
	fakeClaudeOnPath(t, "attested: convention=pass spec=pass correctness=pass scope-minimality=pass")

	var stdout, stderr bytes.Buffer
	code := TicketReview([]string{"--repo", repo, "--ticket", ticketID}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("TicketReview failed: exit %d, stderr=%s", code, stderr.String())
	}
	loaded, err := ticket.Load(filepath.Join(repo, ".golem", "tickets", ticketID))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded.Phase != ticket.PhaseReadyForReview {
		t.Errorf("Phase = %q, want ready-for-review", loaded.Phase)
	}
}

func TestTicketReviewSetsNeedsAttentionOnGateFailure(t *testing.T) {
	repo, _, ticketID := setUpTicketForStepTest(t)
	os.MkdirAll(filepath.Join(repo, ".golem", "roles"), 0o755)
	os.WriteFile(filepath.Join(repo, ".golem", "roles", "reviewer.md"), []byte("# reviewer\n"), 0o644)
	os.WriteFile(filepath.Join(repo, ".golem", "config.yaml"), []byte("backend: claude-code\ngate:\n  commands:\n    - \"false\"\n"), 0o644)
	fakeClaudeOnPath(t, "attested: convention=pass spec=pass correctness=pass scope-minimality=pass")

	var stdout, stderr bytes.Buffer
	code := TicketReview([]string{"--repo", repo, "--ticket", ticketID}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("TicketReview failed: exit %d, stderr=%s", code, stderr.String())
	}
	loaded, err := ticket.Load(filepath.Join(repo, ".golem", "tickets", ticketID))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded.Phase != ticket.PhaseNeedsAttention {
		t.Errorf("Phase = %q, want needs-attention when the gate fails", loaded.Phase)
	}
}
