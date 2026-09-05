package e2e

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/leonp92/golem/internal/agentrunner"
	"github.com/leonp92/golem/internal/blog"
	"github.com/leonp92/golem/internal/observer"
	"github.com/leonp92/golem/internal/ticket"
	"github.com/leonp92/golem/internal/workspace"
)

func initFixtureRepo(t *testing.T) string {
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
	os.WriteFile(filepath.Join(dir, "README.md"), []byte("fixture"), 0o644)
	run("add", "README.md")
	run("commit", "-q", "-m", "initial")
	return dir
}

func TestFullTicketLifecycleWithMockBackend(t *testing.T) {
	repo := initFixtureRepo(t)

	s := ticket.New("e2e-1", "", false)
	worktreePath, branch, err := workspace.Create(repo, "e2e-1", "HEAD")
	if err != nil {
		t.Fatalf("workspace.Create: %v", err)
	}
	s.Branch = branch
	s.WorktreePath = worktreePath
	ticketDir := filepath.Join(repo, ".golem", "tickets", "e2e-1")
	if err := s.Save(ticketDir); err != nil {
		t.Fatalf("Save: %v", err)
	}

	logPath := filepath.Join(ticketDir, "log.jsonl")
	w, err := blog.NewWriter(logPath)
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	w.Append(blog.NewEntry("system", blog.TypeStatus, "ticket created"))
	w.Close()

	os.WriteFile(filepath.Join(worktreePath, "feature.go"), []byte("package main\n"), 0o644)
	commitCmd := exec.Command("git", "add", "feature.go")
	commitCmd.Dir = worktreePath
	if out, err := commitCmd.CombinedOutput(); err != nil {
		t.Fatalf("git add: %v\n%s", err, out)
	}
	commitCmd = exec.Command("git", "commit", "-q", "-m", "add feature")
	commitCmd.Dir = worktreePath
	if out, err := commitCmd.CombinedOutput(); err != nil {
		t.Fatalf("git commit: %v\n%s", err, out)
	}
	shaCmd := exec.Command("git", "rev-parse", "HEAD")
	shaCmd.Dir = worktreePath
	shaOut, err := shaCmd.Output()
	if err != nil {
		t.Fatalf("git rev-parse: %v", err)
	}
	commitSHA := string(shaOut[:7])

	w2, err := blog.NewWriter(logPath)
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	statusEntry := blog.NewEntry("developer", blog.TypeStatus, "committed feature.go")
	statusEntry.CommitSHA = commitSHA
	w2.Append(statusEntry)
	w2.Close()

	mock := agentrunner.NewMock()
	mock.ScriptResponse("convention-enforcer", agentrunner.Result{
		Output: "FINDING: missing package doc comment",
		Model:  "mock-v1",
	})
	obs := observer.New(logPath, mock)
	if err := obs.DispatchForCommit("convention-enforcer", commitSHA, "diff of feature.go", "role prompt"); err != nil {
		t.Fatalf("DispatchForCommit: %v", err)
	}

	entries, err := blog.ReadAll(logPath)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	var sawStatus, sawFinding bool
	for _, e := range entries {
		if e.Type == blog.TypeStatus && e.CommitSHA == commitSHA {
			sawStatus = true
		}
		if e.Type == blog.TypeFinding && e.Model == "mock-v1" {
			sawFinding = true
		}
	}
	if !sawStatus {
		t.Error("expected a STATUS entry for the developer's commit")
	}
	if !sawFinding {
		t.Error("expected a FINDING entry from the observer's dispatched review, tagged with the model that produced it")
	}

	if err := workspace.Remove(repo, worktreePath, branch); err != nil {
		t.Fatalf("workspace.Remove: %v", err)
	}
}
