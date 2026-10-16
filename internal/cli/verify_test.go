package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/leonp92/golem/internal/blog"
	"github.com/leonp92/golem/internal/ticket"
)

// Self-verification runs the gate commands and records the evidence on
// the ticket, pass or fail — the entry is the point, the exit code only
// tells the worker whether to bounce the implementer.
func TestTicketVerifyRecordsEvidence(t *testing.T) {
	repo, _, ticketID := setUpTicketForStepTest(t)
	os.WriteFile(filepath.Join(repo, ".golem", "config.yaml"),
		[]byte("backend: claude-code\ngate:\n  commands:\n    - \"true\"\n"), 0o644)

	var stdout, stderr bytes.Buffer
	if code := TicketVerify([]string{"--repo", repo, "--ticket", ticketID}, &stdout, &stderr); code != 0 {
		t.Fatalf("verify: exit %d, stderr=%s", code, stderr.String())
	}
	entries, err := blog.ReadAll(filepath.Join(repo, ".golem", "tickets", ticketID, "log.jsonl"))
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	found := false
	for _, e := range entries {
		if strings.Contains(e.Message, "self-verification: true") {
			found = true
		}
	}
	if !found {
		t.Fatal("no self-verification entry recorded")
	}
}

func TestTicketVerifyFailsWithEvidence(t *testing.T) {
	repo, _, ticketID := setUpTicketForStepTest(t)
	os.WriteFile(filepath.Join(repo, ".golem", "config.yaml"),
		[]byte("backend: claude-code\ngate:\n  commands:\n    - \"false\"\n"), 0o644)

	var stdout, stderr bytes.Buffer
	if code := TicketVerify([]string{"--repo", repo, "--ticket", ticketID}, &stdout, &stderr); code != 2 {
		t.Fatalf("verify: exit %d, want 2", code)
	}
	entries, _ := blog.ReadAll(filepath.Join(repo, ".golem", "tickets", ticketID, "log.jsonl"))
	found := false
	for _, e := range entries {
		if strings.Contains(e.Message, "self-verification: false") {
			found = true
		}
	}
	if !found {
		t.Fatal("a failed check must still record its evidence")
	}
}

// An epic child's approved acceptance commands run with the gate: a
// failing one fails verification and review alike.
func TestTicketGateRunsAcceptanceCommands(t *testing.T) {
	repo, _, ticketID := setUpTicketForStepTest(t)
	os.WriteFile(filepath.Join(repo, ".golem", "config.yaml"),
		[]byte("backend: claude-code\ngate:\n  commands:\n    - \"true\"\n"), 0o644)
	ticketDir := filepath.Join(repo, ".golem", "tickets", ticketID)
	s, _ := ticket.Load(ticketDir)
	s.AcceptanceCommands = []string{"false"}
	s.Save(ticketDir)

	var stdout, stderr bytes.Buffer
	if code := TicketVerify([]string{"--repo", repo, "--ticket", ticketID}, &stdout, &stderr); code != 2 {
		t.Fatalf("verify with a failing acceptance command: exit %d, want 2", code)
	}
}
