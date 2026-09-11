package worker

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/leonp92/golem/internal/shem/config"
	"github.com/leonp92/golem/internal/ticket"
	"github.com/leonp92/golem/internal/workspace"
)

// TestBuildRevisePrompt verifies the revise prompt embeds the ticket ID,
// worktree path, and feedback verbatim, and does not restate the plan
// (the revise session is scoped to fixes, not a re-implementation).
func TestBuildRevisePrompt(t *testing.T) {
	prompt := buildRevisePrompt("ticket-123", "fix the thing", "the button label is wrong")

	if !strings.Contains(prompt, "ticket-123") {
		t.Error("expected prompt to contain the ticket ID")
	}
	if !strings.Contains(prompt, "the button label is wrong") {
		t.Error("expected prompt to contain the feedback verbatim")
	}
	if !strings.Contains(prompt, ".golem/tickets/ticket-123/worktree/") {
		t.Error("expected prompt to reference the existing worktree path")
	}
	if strings.Contains(prompt, "Plan: .golem/tickets") {
		t.Error("revise prompt must not restate the plan like buildImplementPrompt does")
	}
}

// TestCleanupTicket_UsesStateBranch verifies cleanupTicket removes the
// branch recorded in the ticket's local state.json (the server-computed
// slug branch) rather than re-deriving "ticket/<id>", which would target a
// stale ref and leak the real branch.
func TestCleanupTicket_UsesStateBranch(t *testing.T) {
	repoPath := t.TempDir()
	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = repoPath
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q")
	run("config", "user.email", "test@example.com")
	run("config", "user.name", "test")
	if err := os.WriteFile(filepath.Join(repoPath, "README.md"), []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", "README.md")
	run("commit", "-q", "-m", "initial")

	branch := "ticket/human-friendly-name-abcd1234"
	ticketID := "ticket-uuid-cleanup"
	worktreePath, err := workspace.Create(repoPath, ticketID, branch, "HEAD")
	if err != nil {
		t.Fatalf("workspace.Create: %v", err)
	}
	s := ticket.New(ticketID, "cleanup test", false)
	s.Branch = branch
	s.WorktreePath = worktreePath
	ticketDir := filepath.Join(repoPath, ".golem", "tickets", ticketID)
	if err := s.Save(ticketDir); err != nil {
		t.Fatalf("Save: %v", err)
	}

	cfg := &config.Config{
		Repos: []config.RepoConfig{
			{Path: repoPath, NormalizedRemote: "local/repo"},
		},
	}
	w := &Worker{cfg: cfg, running: make(map[string]context.CancelFunc)}
	w.cleanupTicket("local/repo", ticketID)

	if _, err := os.Stat(worktreePath); !os.IsNotExist(err) {
		t.Error("expected worktree to be removed")
	}
	if err := exec.Command("git", "-C", repoPath, "rev-parse", "--verify", branch).Run(); err == nil {
		t.Errorf("expected branch %q to be deleted", branch)
	}
}
