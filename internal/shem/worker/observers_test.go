package worker

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The observers used to be an instruction in the developer role, run by the
// agent from its own shell. Claude Code strips CLAUDE_CODE_OAUTH_TOKEN from
// that shell, so the nested `claude` every observer needs reported "Not
// logged in" on every ticket and nothing was ever observed. These pin that
// the shem runs them itself, the way it runs the review gate.

func TestRunObserversDispatchesEachRoleOverTheWholeBranch(t *testing.T) {
	repo, worktree, head := observerFixture(t, "convention-enforcer", "spec-adherence")
	calls, envDump := fakeGolemOnPath(t)
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "the-observer-needs-this")
	t.Setenv("GOLEM_GITHUB_TOKEN", "ghp_must_not_reach_the_observer")

	if err := runObservers(context.Background(), repo, worktree, "t-1", "main"); err != nil {
		t.Fatalf("runObservers: %v", err)
	}

	got := readLines(t, calls)
	want := []string{
		"observer dispatch --ticket t-1 --role convention-enforcer --commit " + head + " --base origin/main",
		"observer dispatch --ticket t-1 --role spec-adherence --commit " + head + " --base origin/main",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("golem was run as\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}

	env := strings.Join(readLines(t, envDump), "\n")
	if !strings.Contains(env, "CLAUDE_CODE_OAUTH_TOKEN=the-observer-needs-this") {
		t.Error("the observer did not receive the model credential, so it cannot reach a model")
	}
	if strings.Contains(env, "GOLEM_GITHUB_TOKEN=") {
		t.Error("the observer received golem's push token")
	}
}

// A repository opts out of an observer by deleting its role file; the shem
// has no other way to know which watchers a repository wants.
func TestRunObserversSkipsARoleTheRepositoryRemoved(t *testing.T) {
	repo, worktree, _ := observerFixture(t, "spec-adherence")
	calls, _ := fakeGolemOnPath(t)

	if err := runObservers(context.Background(), repo, worktree, "t-1", ""); err != nil {
		t.Fatalf("runObservers: %v", err)
	}

	got := readLines(t, calls)
	if len(got) != 1 || !strings.Contains(got[0], "--role spec-adherence") {
		t.Errorf("golem was run as %q, want only spec-adherence", got)
	}
	if strings.Contains(got[0], "--base") {
		t.Errorf("no base branch was known, but golem was given one: %q", got[0])
	}
}

func TestRunObserversReportsARoleThatFailed(t *testing.T) {
	repo, worktree, _ := observerFixture(t, "convention-enforcer", "spec-adherence")
	failingGolemOnPath(t)

	err := runObservers(context.Background(), repo, worktree, "t-1", "main")
	if err == nil {
		t.Fatal("an observer that could not run was reported as success")
	}
	for _, role := range []string{"convention-enforcer", "spec-adherence"} {
		if !strings.Contains(err.Error(), role) {
			t.Errorf("error does not name %s: %v", role, err)
		}
	}
}

func TestPromptsLeaveTheObserversToTheShem(t *testing.T) {
	for name, p := range map[string]string{
		"implement": buildImplementPrompt("t-1", "ticket/d-t-1", "d"),
		"revise":    buildRevisePrompt("t-1", "ticket/d-t-1", "d", "fb"),
	} {
		t.Run(name, func(t *testing.T) {
			if !strings.Contains(p, "Do NOT run golem observer dispatch") {
				t.Error("the prompt does not tell the agent the observers are run for it")
			}
		})
	}
}

// observerFixture builds a repository with the given observer role files and
// a worktree holding one commit, and returns that commit.
func observerFixture(t *testing.T, roles ...string) (repo, worktree, head string) {
	t.Helper()
	repo = t.TempDir()
	rolesDir := filepath.Join(repo, ".golem", "roles")
	if err := os.MkdirAll(rolesDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, r := range roles {
		if err := os.WriteFile(filepath.Join(rolesDir, r+".md"), []byte("# "+r+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	worktree = t.TempDir()
	git := func(args ...string) string {
		cmd := exec.Command("git", args...)
		cmd.Dir = worktree
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git("init", "-q")
	git("-c", "user.email=t@example.com", "-c", "user.name=t", "commit", "-q", "--allow-empty", "-m", "work")
	return repo, worktree, git("rev-parse", "HEAD")
}

// fakeGolemOnPath puts a `golem` on PATH that appends its arguments to one
// file and its environment to another, and returns both paths.
func fakeGolemOnPath(t *testing.T) (calls, env string) {
	t.Helper()
	dir := t.TempDir()
	calls = filepath.Join(dir, "calls.txt")
	env = filepath.Join(dir, "env.txt")
	writeFakeGolem(t, dir, "#!/bin/sh\necho \"$@\" >> "+calls+"\nenv >> "+env+"\n")
	return calls, env
}

func failingGolemOnPath(t *testing.T) {
	t.Helper()
	writeFakeGolem(t, t.TempDir(), "#!/bin/sh\necho 'Not logged in' >&2\nexit 1\n")
}

func writeFakeGolem(t *testing.T, dir, script string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "golem"), []byte(script), 0o755); err != nil {
		t.Fatalf("write fake golem: %v", err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func readLines(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return strings.Split(strings.TrimRight(string(data), "\n"), "\n")
}
