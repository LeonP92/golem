package worker

import (
	"context"
	"os/exec"
	"path/filepath"
	"testing"
)

// TestPushTicketBranch pushes a branch into a local bare repo acting as origin.
func TestPushTicketBranch(t *testing.T) {
	root := t.TempDir()
	origin := filepath.Join(root, "origin.git")
	work := filepath.Join(root, "work")

	run := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}

	run(root, "init", "--bare", origin)
	run(root, "init", work)
	run(work, "config", "user.email", "test@example.com")
	run(work, "config", "user.name", "Test")
	run(work, "commit", "--allow-empty", "-m", "base")
	run(work, "checkout", "-b", "ticket/x")
	run(work, "commit", "--allow-empty", "-m", "work")
	run(work, "remote", "add", "origin", origin)

	if err := pushTicketBranch(context.Background(), work, "ticket/x"); err != nil {
		t.Fatalf("pushTicketBranch: %v", err)
	}

	cmd := exec.Command("git", "--git-dir", origin, "rev-parse", "ticket/x")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("branch not present on origin: %v\n%s", err, out)
	}
}

// TestPushTicketBranchReportsFailure asserts a push to a missing remote is a
// returned error, not a silent success — the caller must not report the branch
// as pushed.
func TestPushTicketBranchReportsFailure(t *testing.T) {
	work := t.TempDir()
	cmd := exec.Command("git", "init")
	cmd.Dir = work
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	if err := pushTicketBranch(context.Background(), work, "ticket/x"); err == nil {
		t.Fatal("pushTicketBranch returned nil with no origin configured")
	}
}
