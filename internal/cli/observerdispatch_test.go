package cli

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/leonp92/golem/internal/blog"
)

func TestObserverDispatchAppendsFindingFromRealCommit(t *testing.T) {
	repo, worktree, ticketID := setUpTicketForStepTest(t)
	os.WriteFile(filepath.Join(repo, ".golem", "config.yaml"), []byte("backend: claude-code\n"), 0o644)
	os.MkdirAll(filepath.Join(repo, ".golem", "roles"), 0o755)
	os.WriteFile(filepath.Join(repo, ".golem", "roles", "convention-enforcer.md"), []byte("# role\n"), 0o644)

	sha := gitCommit(t, worktree, "feature.go", "package main\n", "add feature")

	fakeClaudeOnPath(t, "FINDING: missing doc comment")

	var stdout, stderr bytes.Buffer
	code := ObserverDispatch([]string{"--repo", repo, "--ticket", ticketID, "--role", "convention-enforcer", "--commit", sha}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("ObserverDispatch failed: exit %d, stderr=%s", code, stderr.String())
	}

	entries, err := blog.ReadAll(filepath.Join(repo, ".golem", "tickets", ticketID, "log.jsonl"))
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	found := false
	for _, e := range entries {
		if e.Type == blog.TypeFinding && strings.Contains(e.Message, "missing doc comment") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected a FINDING entry from the dispatched review, got %+v", entries)
	}
}

// A ticket is usually several commits. Reviewing only `git show <head>` let
// every commit but the last reach ready-for-review unseen, so --base widens
// the review to the whole branch.
func TestObserverDispatchWithBaseReviewsEveryCommitOnTheBranch(t *testing.T) {
	repo, worktree, ticketID := setUpTicketForStepTest(t)
	os.WriteFile(filepath.Join(repo, ".golem", "config.yaml"), []byte("backend: claude-code\n"), 0o644)
	os.MkdirAll(filepath.Join(repo, ".golem", "roles"), 0o755)
	os.WriteFile(filepath.Join(repo, ".golem", "roles", "convention-enforcer.md"), []byte("# role\n"), 0o644)

	base, err := gitHead(worktree)
	if err != nil {
		t.Fatalf("gitHead: %v", err)
	}
	gitCommit(t, worktree, "first.go", "package first\n", "first step")
	head := gitCommit(t, worktree, "second.go", "package second\n", "second step")

	prompt := fakeClaudeRecordingPrompt(t, "FINDING: noted")

	var stdout, stderr bytes.Buffer
	code := ObserverDispatch([]string{"--repo", repo, "--ticket", ticketID, "--role", "convention-enforcer",
		"--commit", head, "--base", base}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("ObserverDispatch failed: exit %d, stderr=%s", code, stderr.String())
	}

	got, err := os.ReadFile(prompt)
	if err != nil {
		t.Fatalf("read the prompt the observer was given: %v", err)
	}
	for _, want := range []string{"first.go", "second.go"} {
		if !strings.Contains(string(got), want) {
			t.Errorf("observer prompt does not include %s, so that commit went unreviewed", want)
		}
	}
}

// A base the checkout does not have (a stacked ticket's parent branch that was
// never fetched) must not cost the ticket its review: fall back to the commit.
func TestObserverDispatchFallsBackToTheCommitWhenTheBaseIsUnknown(t *testing.T) {
	repo, worktree, ticketID := setUpTicketForStepTest(t)
	os.WriteFile(filepath.Join(repo, ".golem", "config.yaml"), []byte("backend: claude-code\n"), 0o644)
	os.MkdirAll(filepath.Join(repo, ".golem", "roles"), 0o755)
	os.WriteFile(filepath.Join(repo, ".golem", "roles", "convention-enforcer.md"), []byte("# role\n"), 0o644)
	head := gitCommit(t, worktree, "only.go", "package only\n", "only step")
	prompt := fakeClaudeRecordingPrompt(t, "FINDING: noted")

	var stdout, stderr bytes.Buffer
	code := ObserverDispatch([]string{"--repo", repo, "--ticket", ticketID, "--role", "convention-enforcer",
		"--commit", head, "--base", "origin/no-such-branch"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("ObserverDispatch failed: exit %d, stderr=%s", code, stderr.String())
	}
	if got, _ := os.ReadFile(prompt); !strings.Contains(string(got), "only.go") {
		t.Error("with an unknown base the commit itself was not reviewed")
	}
	if !strings.Contains(stderr.String(), "origin/no-such-branch") {
		t.Errorf("the fallback was silent; stderr=%q", stderr.String())
	}
}

// A phase that made no commits leaves nothing to review, and a model asked to
// review nothing can still invent a finding that then sits in the log.
func TestObserverDispatchSkipsAnEmptyDiff(t *testing.T) {
	repo, worktree, ticketID := setUpTicketForStepTest(t)
	os.WriteFile(filepath.Join(repo, ".golem", "config.yaml"), []byte("backend: claude-code\n"), 0o644)
	os.MkdirAll(filepath.Join(repo, ".golem", "roles"), 0o755)
	os.WriteFile(filepath.Join(repo, ".golem", "roles", "convention-enforcer.md"), []byte("# role\n"), 0o644)
	head, err := gitHead(worktree)
	if err != nil {
		t.Fatalf("gitHead: %v", err)
	}
	prompt := fakeClaudeRecordingPrompt(t, "FINDING: invented")

	var stdout, stderr bytes.Buffer
	code := ObserverDispatch([]string{"--repo", repo, "--ticket", ticketID, "--role", "convention-enforcer",
		"--commit", head, "--base", head}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("ObserverDispatch failed: exit %d, stderr=%s", code, stderr.String())
	}
	if _, err := os.Stat(prompt); err == nil {
		t.Error("an observer was dispatched over an empty diff")
	}
}

// fakeClaudeRecordingPrompt is fakeClaudeOnPath that also saves the prompt it
// was sent on stdin, and returns where.
func fakeClaudeRecordingPrompt(t *testing.T, output string) string {
	t.Helper()
	dir := t.TempDir()
	promptPath := filepath.Join(dir, "prompt.txt")
	src := `package main

import (
	"fmt"
	"io"
	"os"
)

func main() {
	in, _ := io.ReadAll(os.Stdin)
	os.WriteFile(` + "`" + promptPath + "`" + `, in, 0o644)
	fmt.Println(` + "`" + output + "`" + `)
}
`
	buildFakeClaude(t, dir, src)
	return promptPath
}

// fakeClaudeOnPath builds a small Go binary that prints output and places it
// on PATH as "claude" (or "claude.exe" on Windows). The output argument is
// the text the fake should print to stdout.
func fakeClaudeOnPath(t *testing.T, output string) {
	t.Helper()
	dir := t.TempDir()

	// Write a tiny Go main program that just prints the desired output.
	src := `package main

import "fmt"

func main() {
	fmt.Println(` + "`" + output + "`" + `)
}
`
	buildFakeClaude(t, dir, src)
}

// buildFakeClaude compiles src into dir as "claude" and puts dir first on PATH.
func buildFakeClaude(t *testing.T, dir, src string) {
	t.Helper()
	srcPath := filepath.Join(dir, "fakeclaude.go")
	if err := os.WriteFile(srcPath, []byte(src), 0o644); err != nil {
		t.Fatalf("WriteFile fakeclaude.go: %v", err)
	}

	binName := "claude"
	if runtime.GOOS == "windows" {
		binName = "claude.exe"
	}
	binPath := filepath.Join(dir, binName)
	cmd := exec.Command("go", "build", "-o", binPath, srcPath)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("building fake claude: %v\n%s", err, out)
	}

	oldPath := os.Getenv("PATH")
	os.Setenv("PATH", dir+string(os.PathListSeparator)+oldPath)
	t.Cleanup(func() { os.Setenv("PATH", oldPath) })
	if _, err := exec.LookPath("claude"); err != nil {
		t.Fatalf("fake claude not on PATH: %v", err)
	}
}
