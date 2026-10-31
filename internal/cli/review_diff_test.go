package cli

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/leonp92/golem/internal/blog"
	"github.com/leonp92/golem/internal/evidence"
	"github.com/leonp92/golem/internal/ticket"
)

// The reviewer used to get an empty <diff>, and models took that literally:
// "the provided diff is empty, I cannot attest correctness".
func TestTicketReviewGivesTheReviewerTheBranchDiff(t *testing.T) {
	repo, worktree, ticketID := setUpTicketForStepTest(t)
	writeReviewerSetup(t, repo)
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

func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func commitGo(t *testing.T, dir, name string) {
	t.Helper()
	os.WriteFile(filepath.Join(dir, name), []byte("package x // "+name+"\n"), 0o644)
	gitIn(t, dir, "add", name)
	gitIn(t, dir, "commit", "-q", "-m", name)
}

// capturePrompt installs a fake claude that records the reviewer prompt.
func capturePrompt(t *testing.T) string {
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
	return prompt
}

// The pinned BranchBaseSHA goes stale as siblings merge; the reviewer must see
// only this ticket's own change.
func TestReviewDiffUsesLiveBaseNotStalePin(t *testing.T) {
	repo, worktree, id := setUpTicketForStepTest(t)
	writeReviewerSetup(t, repo)
	branch := gitIn(t, repo, "rev-parse", "--abbrev-ref", "HEAD")
	pin := gitIn(t, worktree, "rev-parse", "HEAD")
	s, _ := ticket.Load(filepath.Join(repo, ".golem", "tickets", id))
	s.BranchBaseSHA = pin
	if err := s.Save(filepath.Join(repo, ".golem", "tickets", id)); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"sib1.go", "sib2.go", "sib3.go"} {
		commitGo(t, repo, f)
	}
	gitIn(t, worktree, "fetch", "-q", "origin")
	gitIn(t, worktree, "merge", "-q", "--no-edit", "origin/"+branch)
	commitGo(t, worktree, "own.go")
	prompt := capturePrompt(t)

	var stdout, stderr bytes.Buffer
	if code := TicketReview([]string{"--repo", repo, "--ticket", id, "--base", "origin/" + branch}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
	got, _ := os.ReadFile(prompt)
	if !strings.Contains(string(got), "own.go") {
		t.Errorf("prompt lacks the ticket's own change:\n%s", got)
	}
	if strings.Contains(string(got), "sib1.go") || strings.Contains(string(got), "sib3.go") {
		t.Errorf("prompt carries merged siblings' work:\n%s", got)
	}
}

func TestCollapseLongEntries(t *testing.T) {
	long := strings.Repeat("a", 3000) + strings.Repeat("z", 20000) + strings.Repeat("b", 3000)
	in := []blog.Entry{{Type: blog.TypeStatus, Message: long}, {Type: blog.TypeFinding, Message: long}, {Type: blog.TypeBlocker, Message: "short"}}
	out := collapseLongEntries(in, 4096)
	if len(out[0].Message) > 4200 || !strings.Contains(out[0].Message, "truncated") ||
		!strings.HasPrefix(out[0].Message, "aaa") || !strings.HasSuffix(out[0].Message, "bbb") {
		t.Errorf("status not collapsed to head+tail: %d bytes", len(out[0].Message))
	}
	if out[1].Message != long || out[2].Message != "short" {
		t.Error("non-STATUS/BLOCKER or short entry changed")
	}
	if len(in[0].Message) != len(long) {
		t.Error("input mutated")
	}
}

// From inside a ticket worktree, emit must land in the primary repo's log.
func TestLogEmitFromLinkedWorktreeLandsInRealLog(t *testing.T) {
	repo := initRepoForCLI(t)
	wt := filepath.Join(t.TempDir(), "wt")
	gitIn(t, repo, "worktree", "add", "-q", "-b", "tk", wt)
	var stdout, stderr bytes.Buffer
	code := LogEmit([]string{"--repo", wt, "--ticket", "t9", "--role", "implementer", "--type", "STATUS", "hello"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
	entries, err := blog.ReadAll(filepath.Join(repo, ".golem", "tickets", "t9", "log.jsonl"))
	if err != nil || len(entries) != 1 {
		t.Fatalf("primary log: %v %v", entries, err)
	}
	if _, err := os.Stat(filepath.Join(wt, ".golem", "tickets", "t9", "log.jsonl")); err == nil {
		t.Error("a private log was also written in the worktree")
	}
}

// Over the byte cap no reviewer runs (it would see only a part, and its verdict
// would read as a whole review): the failure is an environment one that names the
// size and the fix, whatever the provider says.
func TestTicketReviewOversizedDiffIsEnvironmentFailure(t *testing.T) {
	repo, worktree, id := setUpTicketForStepTest(t)
	writeReviewerSetup(t, repo)
	cfgPath := filepath.Join(repo, ".golem", "config.yaml")
	cfg, _ := os.ReadFile(cfgPath)
	os.WriteFile(cfgPath, append(cfg, []byte("review:\n  max_diff_bytes: 50\n")...), 0o644)
	commitGo(t, worktree, "big.go")
	prompt := capturePrompt(t)

	var stdout, stderr bytes.Buffer
	code := TicketReview([]string{"--repo", repo, "--ticket", id, "--base", "HEAD~1"}, &stdout, &stderr)
	if code == 0 {
		t.Fatal("oversized diff was approved")
	}
	for _, want := range []string{evidence.EnvironmentMarker, "review diff too large (", "max_diff_bytes"} {
		if !strings.Contains(stderr.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, stderr.String())
		}
	}
	if _, err := os.Stat(prompt); err == nil {
		t.Error("a reviewer ran on a diff it could not read whole")
	}
	s, _ := ticket.Load(filepath.Join(repo, ".golem", "tickets", id))
	if s.Phase != ticket.PhaseNeedsAttention {
		t.Errorf("phase %s", s.Phase)
	}
	entries, _ := blog.ReadAll(filepath.Join(repo, ".golem", "tickets", id, "log.jsonl"))
	if last := entries[len(entries)-1]; last.Role != "reviewer" || !strings.HasPrefix(last.Message, evidence.EnvironmentMarker) {
		t.Errorf("reviewer log entry must start with the environment marker: %+v", last)
	}
}

func TestReviewBaseNeedsAResolvableLiveBase(t *testing.T) {
	_, worktree, _ := setUpTicketForStepTest(t)
	pinned, _ := revSHA(worktree, "HEAD")
	if got := reviewBase(worktree, "no/such/branch", pinned); got != pinned {
		t.Errorf("unresolvable --base used instead of the pin: %q", got)
	}
	gitIn(t, worktree, "branch", "only-local")
	if got := reviewBase(worktree, "origin/only-local", pinned); got != "only-local" {
		t.Errorf("origin/ base without an origin should fall back to the local name: %q", got)
	}
	if got := reviewBase(worktree, "", pinned); got != pinned {
		t.Errorf("no --base: %q", got)
	}
}
