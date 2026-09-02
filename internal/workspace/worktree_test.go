package workspace

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func initTestRepo(t *testing.T) string {
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
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("hi"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	run("add", "README.md")
	run("commit", "-q", "-m", "initial")
	return dir
}

func TestCreateAndRemoveWorktree(t *testing.T) {
	repo := initTestRepo(t)

	worktreePath, branch, err := Create(repo, "t1", "HEAD")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if branch != "ticket/t1" {
		t.Errorf("branch = %q, want ticket/t1", branch)
	}
	if _, err := os.Stat(worktreePath); err != nil {
		t.Fatalf("worktree path does not exist: %v", err)
	}
	if _, err := os.Stat(filepath.Join(worktreePath, "README.md")); err != nil {
		t.Fatalf("worktree missing checked-out file: %v", err)
	}

	if err := Remove(repo, worktreePath, branch); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if _, err := os.Stat(worktreePath); !os.IsNotExist(err) {
		t.Fatal("worktree path should be gone after Remove")
	}
}
