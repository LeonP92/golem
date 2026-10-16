package cli

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The reviewer used to get an empty <diff>, and models took that literally:
// "the provided diff is empty, I cannot attest correctness".
func TestTicketReviewGivesTheReviewerTheBranchDiff(t *testing.T) {
	repo, worktree, ticketID := setUpTicketForStepTest(t)
	os.MkdirAll(filepath.Join(repo, ".golem", "roles"), 0o755)
	os.WriteFile(filepath.Join(repo, ".golem", "roles", "reviewer.md"), []byte("# reviewer\n"), 0o644)
	os.WriteFile(filepath.Join(repo, ".golem", "config.yaml"), []byte("backend: claude-code\ngate:\n  commands: []\n"), 0o644)
	os.WriteFile(filepath.Join(worktree, "feature.go"), []byte("package feature\n"), 0o644)
	for _, args := range [][]string{{"add", "feature.go"}, {"commit", "-q", "-m", "feature"}} {
		if out, err := exec.Command("git", append([]string{"-C", worktree}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	prompt := filepath.Join(t.TempDir(), "prompt.txt")
	buildFakeClaude(t, t.TempDir(), `package main

import (
	"fmt"
	"io"
	"os"
)

func main() {
	b, _ := io.ReadAll(os.Stdin)
	os.WriteFile(`+"`"+prompt+"`"+`, b, 0o644)
	fmt.Println("attested")
}
`)

	var stdout, stderr bytes.Buffer
	if code := TicketReview([]string{"--repo", repo, "--ticket", ticketID, "--base", "HEAD~1"}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
	got, _ := os.ReadFile(prompt)
	if !strings.Contains(string(got), "+package feature") {
		t.Errorf("reviewer prompt lacks the branch diff:\n%s", got)
	}
}

// A local:// repository has no origin, so origin/<base> falls back to the
// local branch rather than to the last commit alone.
func TestReviewDiffFallsBackToLocalBase(t *testing.T) {
	_, worktree, _ := setUpTicketForStepTest(t)
	run := func(args ...string) string {
		out, err := exec.Command("git", append([]string{"-C", worktree}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	run("branch", "localbase")
	run("remote", "remove", "origin")
	for _, f := range []string{"one.txt", "two.txt"} {
		os.WriteFile(filepath.Join(worktree, f), []byte(f+"\n"), 0o644)
		run("add", f)
		run("commit", "-q", "-m", f)
	}
	diff, err := reviewDiff(worktree, "origin/localbase", run("rev-parse", "HEAD"), &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(diff, "one.txt") || !strings.Contains(diff, "two.txt") {
		t.Errorf("diff does not cover the whole branch:\n%s", diff)
	}
}
