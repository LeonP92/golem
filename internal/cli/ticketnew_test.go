package cli

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/leonpham/golem/internal/blog"
	"github.com/leonpham/golem/internal/ticket"
)

func initRepoForCLI(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q")
	run("config", "user.email", "test@example.com")
	run("config", "user.name", "test")
	os.WriteFile(filepath.Join(dir, "README.md"), []byte("hi"), 0o644)
	golemDir := filepath.Join(dir, ".golem")
	os.MkdirAll(filepath.Join(golemDir, "tickets"), 0o755)
	os.WriteFile(filepath.Join(golemDir, "config.yaml"), []byte("backend: claude-code\ngate:\n  commands: []\n"), 0o644)
	run("add", ".")
	run("commit", "-q", "-m", "initial")
	return dir
}

func TestTicketNewCreatesStateWorktreeAndLog(t *testing.T) {
	repo := initRepoForCLI(t)
	var stdout, stderr bytes.Buffer

	code := TicketNew([]string{"--repo", repo, "--id", "t1", "add-widget"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("TicketNew failed: exit %d, stderr=%s", code, stderr.String())
	}

	ticketDir := filepath.Join(repo, ".golem", "tickets", "t1")
	s, err := ticket.Load(ticketDir)
	if err != nil {
		t.Fatalf("ticket.Load: %v", err)
	}
	if s.Branch != "ticket/t1" {
		t.Errorf("Branch = %q, want ticket/t1", s.Branch)
	}
	if _, err := os.Stat(s.WorktreePath); err != nil {
		t.Fatalf("worktree missing: %v", err)
	}

	entries, err := blog.ReadAll(filepath.Join(ticketDir, "log.jsonl"))
	if err != nil {
		t.Fatalf("blog.ReadAll: %v", err)
	}
	if len(entries) != 1 || entries[0].Type != blog.TypeStatus {
		t.Fatalf("expected one STATUS entry, got %+v", entries)
	}
}

func TestTicketNewTrivialFlagSkipsBrainstorm(t *testing.T) {
	repo := initRepoForCLI(t)
	var stdout, stderr bytes.Buffer

	code := TicketNew([]string{"--repo", repo, "--id", "t2", "--trivial", "fix-typo"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("TicketNew failed: exit %d, stderr=%s", code, stderr.String())
	}
	s, err := ticket.Load(filepath.Join(repo, ".golem", "tickets", "t2"))
	if err != nil {
		t.Fatalf("ticket.Load: %v", err)
	}
	if s.Phase != ticket.PhasePlan {
		t.Errorf("Phase = %q, want plan for a trivial ticket", s.Phase)
	}
}
