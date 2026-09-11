package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/leonp92/golem/internal/blog"
	"github.com/leonp92/golem/internal/ticket"
	"github.com/leonp92/golem/internal/workspace"
)

func setUpRealWorktreeTicket(t *testing.T) (repo, ticketID string) {
	t.Helper()
	repo = initRepoForCLI(t)
	ticketID = "t1"
	s := ticket.New(ticketID, "", false)
	branch := "ticket/" + ticketID
	worktreePath, err := workspace.Create(repo, ticketID, branch, "HEAD")
	if err != nil {
		t.Fatalf("workspace.Create: %v", err)
	}
	s.Branch = branch
	s.WorktreePath = worktreePath
	if err := s.Save(filepath.Join(repo, ".golem", "tickets", ticketID)); err != nil {
		t.Fatalf("Save: %v", err)
	}
	os.MkdirAll(filepath.Join(repo, ".golem", "roles"), 0o755)
	os.WriteFile(filepath.Join(repo, ".golem", "roles", "reviewer.md"), []byte("# reviewer\n"), 0o644)
	os.WriteFile(filepath.Join(repo, ".golem", "config.yaml"), []byte("backend: claude-code\n"), 0o644)
	return repo, ticketID
}

func TestTicketCloseTearsDownWorktreeAndSetsClosed(t *testing.T) {
	repo, ticketID := setUpRealWorktreeTicket(t)

	var stdout, stderr bytes.Buffer
	code := TicketClose([]string{"--repo", repo, "--ticket", ticketID}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("TicketClose failed: exit %d, stderr=%s", code, stderr.String())
	}

	loaded, err := ticket.Load(filepath.Join(repo, ".golem", "tickets", ticketID))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded.Phase != ticket.PhaseClosed {
		t.Errorf("Phase = %q, want closed", loaded.Phase)
	}
	if _, statErr := os.Stat(loaded.WorktreePath); !os.IsNotExist(statErr) {
		t.Error("expected worktree to be removed")
	}
}

func TestTicketClosePromotesSoulEntryFromDivergence(t *testing.T) {
	repo, ticketID := setUpRealWorktreeTicket(t)
	ticketDir := filepath.Join(repo, ".golem", "tickets", ticketID)

	w, err := blog.NewWriter(filepath.Join(ticketDir, "log.jsonl"))
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	blocker := blog.NewEntry("convention-enforcer", blog.TypeBlocker, "duplicate validation logic")
	blocker.ID = "b1"
	w.Append(blocker)
	resolved := blog.NewEntry("human", blog.TypeResolved, "should have extended ValidateInput's params instead of writing a new function")
	resolved.InReplyTo = "b1"
	w.Append(resolved)
	w.Close()

	fakeClaudeOnPath(t, "SOUL:extend-over-duplicate.md:when introducing a new function, check whether an existing function's parameters could be extended to cover it before duplicating logic")

	var stdout, stderr bytes.Buffer
	code := TicketClose([]string{"--repo", repo, "--ticket", ticketID}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("TicketClose failed: exit %d, stderr=%s", code, stderr.String())
	}

	data, err := os.ReadFile(filepath.Join(repo, ".golem", "wiki", "soul", "extend-over-duplicate.md"))
	if err != nil {
		t.Fatalf("expected soul entry to be written: %v", err)
	}
	if string(data) == "" {
		t.Error("soul entry content should not be empty")
	}

	entries, err := blog.ReadAll(filepath.Join(ticketDir, "log.jsonl"))
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	found := false
	for _, e := range entries {
		if e.Type == blog.TypeStatus && e.Message == "promoted soul entry: extend-over-duplicate.md" {
			found = true
		}
	}
	if !found {
		t.Fatal("expected a logged STATUS entry recording the promotion, even without a human approval gate")
	}
}

func TestTicketCloseIgnoresMalformedSoulLines(t *testing.T) {
	repo, ticketID := setUpRealWorktreeTicket(t)
	ticketDir := filepath.Join(repo, ".golem", "tickets", ticketID)

	w, err := blog.NewWriter(filepath.Join(ticketDir, "log.jsonl"))
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	blocker := blog.NewEntry("convention-enforcer", blog.TypeBlocker, "x")
	blocker.ID = "b1"
	w.Append(blocker)
	resolved := blog.NewEntry("human", blog.TypeResolved, "y")
	resolved.InReplyTo = "b1"
	w.Append(resolved)
	w.Close()

	fakeClaudeOnPath(t, "SOUL:missing-content-field")

	var stdout, stderr bytes.Buffer
	code := TicketClose([]string{"--repo", repo, "--ticket", ticketID}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("TicketClose failed: exit %d, stderr=%s", code, stderr.String())
	}

	entries, _ := os.ReadDir(filepath.Join(repo, ".golem", "wiki", "soul"))
	if len(entries) != 0 {
		t.Errorf("expected no soul files written from a malformed proposal, got %v", entries)
	}
}
