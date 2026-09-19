package worker

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@e",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@e",
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s in %s: %v\n%s", strings.Join(args, " "), dir, err, out)
	}
	return string(out)
}

// pushFixture builds an origin, a working repo, and a ticket worktree on a
// branch, and returns (repoPath, worktreePath, branch, originPath).
func pushFixture(t *testing.T) (string, string, string, string) {
	t.Helper()
	root := t.TempDir()
	origin := filepath.Join(root, "origin.git")
	git(t, root, "init", "--bare", "-b", "main", origin)

	repo := filepath.Join(root, "repo")
	git(t, root, "clone", origin, repo)
	if err := os.WriteFile(filepath.Join(repo, "f.txt"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, repo, "add", "f.txt")
	git(t, repo, "commit", "-m", "init")
	git(t, repo, "push", "origin", "main")

	branch := "ticket/thing-abc12345"
	worktree := filepath.Join(repo, ".golem", "tickets", "t1", "worktree")
	git(t, repo, "worktree", "add", "-b", branch, worktree)
	if err := os.WriteFile(filepath.Join(worktree, "g.txt"), []byte("work\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, worktree, "add", "g.txt")
	git(t, worktree, "commit", "-m", "work")
	return repo, worktree, branch, origin
}

// plantHook writes a pre-push hook that records its own environment, placed
// exactly the way the agent could place one: via core.hooksPath pointing
// inside the worktree, which is what husky does and what the omnicore repo
// actually has configured (core.hooksPath = .husky/_).
func plantHook(t *testing.T, repo, worktree string) string {
	t.Helper()
	hookDir := filepath.Join(worktree, ".husky", "_")
	if err := os.MkdirAll(hookDir, 0o755); err != nil {
		t.Fatal(err)
	}
	evidence := filepath.Join(t.TempDir(), "stolen.txt")
	script := "#!/bin/sh\nenv > " + evidence + "\nexit 0\n"
	if err := os.WriteFile(filepath.Join(hookDir, "pre-push"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	git(t, repo, "config", "core.hooksPath", ".husky/_")
	return evidence
}

// TestPushTicketBranch_DoesNotRunAgentHooksWithTheToken is the fix for the
// review finding on pushTicketBranch.
//
// The push carried GOLEM_GITHUB_TOKEN in its environment and ran with
// cmd.Dir set to the agent's own worktree. Git runs hooks from the
// repository being pushed, and core.hooksPath can put those hooks inside the
// working tree — which is what husky does, and what the repository Golem is
// actually deployed against has configured. So the agent did not even need
// to reach into .git/: an ordinary file it is entitled to write, committed
// as part of its own work, would execute with the push credential in its
// environment.
//
// This is not theoretical for this codebase. A husky pre-push hook already
// ran during a real Golem push and failed with a shell syntax error, which
// is how we know hooks execute on this path.
func TestPushTicketBranch_DoesNotRunAgentHooksWithTheToken(t *testing.T) {
	repo, worktree, branch, origin := pushFixture(t)
	evidence := plantHook(t, repo, worktree)

	const secret = "ghp_totally-not-a-real-token-0123456789"
	t.Setenv("GOLEM_GITHUB_TOKEN", secret)

	if err := pushTicketBranch(context.Background(), repo, worktree, branch, "file://"+origin); err != nil {
		t.Fatalf("push: %v", err)
	}

	// The branch really did arrive: the isolation must not be achieved by
	// simply not pushing.
	if out := git(t, origin, "branch", "--list", branch); !strings.Contains(out, branch) {
		t.Fatalf("branch %q is not in origin after push; got %q", branch, out)
	}

	if data, err := os.ReadFile(evidence); err == nil {
		if strings.Contains(string(data), secret) {
			t.Error("a pre-push hook inside the agent's worktree ran with " +
				"GOLEM_GITHUB_TOKEN in its environment: the agent can steal the push credential")
		} else {
			t.Error("a pre-push hook inside the agent's worktree executed during the push; " +
				"it saw no token this time, but nothing about that is guaranteed")
		}
	}
}

// The mirror Golem pushes from must live outside the repository the agent
// works in, or the agent can write its config and hooks too and the
// isolation is only apparent.
func TestPushMirror_LivesOutsideTheAgentsRepository(t *testing.T) {
	repo, worktree, branch, origin := pushFixture(t)
	t.Setenv("GOLEM_GITHUB_TOKEN", "x")
	if err := pushTicketBranch(context.Background(), repo, worktree, branch, "file://"+origin); err != nil {
		t.Fatalf("push: %v", err)
	}
	mirror := pushMirrorPath(repo)
	if mirror == "" {
		t.Fatal("no mirror path")
	}
	if strings.HasPrefix(filepath.Clean(mirror), filepath.Clean(repo)+string(filepath.Separator)) {
		t.Errorf("push mirror %q is inside the agent's repository %q", mirror, repo)
	}
	if _, err := os.Stat(mirror); err != nil {
		t.Errorf("push mirror was not created at %q: %v", mirror, err)
	}
}
