package forge

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// lab is a scratch remote + mirror pair on the local filesystem.
type lab struct {
	t      *testing.T
	remote string // bare repo standing in for "any git remote"
	mirror string
}

func newLab(t *testing.T) *lab {
	t.Helper()
	root := t.TempDir()
	l := &lab{t: t, remote: filepath.Join(root, "remote.git"), mirror: filepath.Join(root, "mirror.git")}
	l.git("", "init", "--bare", l.remote)
	return l
}

func (l *lab) git(dir string, args ...string) string {
	l.t.Helper()
	cmd := exec.Command("git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	out, err := cmd.CombinedOutput()
	if err != nil {
		l.t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

// seed creates a repo with a base commit on main plus one commit per given
// branch (each touching its own file), and pushes everything to the remote.
func (l *lab) seed(branches ...string) map[string]string {
	l.t.Helper()
	work := filepath.Join(l.t.TempDir(), "work")
	l.git("", "init", work)
	// Reusing the lab must reuse its base, not recreate a commit whose
	// timestamp may differ when a test crosses a wall-clock second.
	if l.git(work, "ls-remote", l.remote, "refs/heads/main") != "" {
		l.git(work, "fetch", "-q", l.remote, "refs/heads/main")
		l.git(work, "checkout", "-q", "-b", "main", "FETCH_HEAD")
	} else {
		l.git(work, "checkout", "-q", "-b", "main")
		os.WriteFile(filepath.Join(work, "base.txt"), []byte("base\n"), 0o644)
		l.git(work, "add", ".")
		l.git(work, "commit", "-qm", "base")
		l.git(work, "push", "-q", l.remote, "main")
	}
	heads := map[string]string{}
	for _, b := range branches {
		l.git(work, "checkout", "-q", "-b", b, "main")
		os.WriteFile(filepath.Join(work, strings.ReplaceAll(b, "/", "_")+".txt"), []byte(b+"\n"), 0o644)
		l.git(work, "add", ".")
		l.git(work, "commit", "-qm", b)
		l.git(work, "push", "-q", l.remote, b)
		heads[b] = l.git(work, "rev-parse", "HEAD")
		l.git(work, "checkout", "-q", "main")
	}
	return heads
}

func (l *lab) remoteSHA(ref string) string {
	l.t.Helper()
	return l.git("", "-C", l.remote, "rev-parse", "refs/heads/"+ref)
}

func TestMergeIntoTarget(t *testing.T) {
	ctx := context.Background()
	l := newLab(t)
	heads := l.seed("feat")

	g := Git{}
	head, err := g.HeadSHA(ctx, l.remote, "feat")
	if err != nil || head != heads["feat"] {
		t.Fatalf("HeadSHA = %q, %v; want %q", head, err, heads["feat"])
	}
	// A missing target is created at the pinned head: the run's first
	// child merges before the target ref exists anywhere.
	res, err := g.MergeIntoTarget(ctx, Merge{Mirror: l.mirror, Remote: l.remote,
		Branch: "feat", HeadSHA: heads["feat"], TargetRef: "epic/abc"})
	if err != nil || res.Status != Merged || res.SHA != heads["feat"] {
		t.Fatalf("create target: got %v %v", res, err)
	}
	if l.remoteSHA("epic/abc") != heads["feat"] {
		t.Fatalf("created target = %q, want the child head %q", l.remoteSHA("epic/abc"), heads["feat"])
	}
	// A second child on top merges with both parents. Rewind the target
	// to main first so the merge has something to do.
	l.git("", "-C", l.remote, "update-ref", "refs/heads/epic/abc", "main")

	res, err = g.MergeIntoTarget(ctx, Merge{Mirror: l.mirror, Remote: l.remote,
		Branch: "feat", HeadSHA: heads["feat"], TargetRef: "epic/abc"})
	if err != nil || res.Status != Merged {
		t.Fatalf("merge: got %v %v", res, err)
	}
	if res.SHA == "" || l.remoteSHA("epic/abc") != res.SHA {
		t.Fatalf("remote target = %q, merge sha = %q", l.remoteSHA("epic/abc"), res.SHA)
	}
	// The merge commit has both parents: the old target and the child head.
	parents := l.git("", "-C", l.remote, "rev-list", "--parents", "-n", "1", res.SHA)
	if !strings.Contains(parents, heads["feat"]) {
		t.Fatalf("merge commit parents %q do not include the child head", parents)
	}

	// A retry sees the head already contained and only owes the receipt.
	res, err = g.MergeIntoTarget(ctx, Merge{Mirror: l.mirror, Remote: l.remote,
		Branch: "feat", HeadSHA: heads["feat"], TargetRef: "epic/abc"})
	if err != nil || res.Status != Contained || res.SHA == "" {
		t.Fatalf("contained retry: got %v %v", res, err)
	}

	// A stale pin is reported, never merged.
	res, err = g.MergeIntoTarget(ctx, Merge{Mirror: l.mirror, Remote: l.remote,
		Branch: "feat", HeadSHA: strings.Repeat("0", 40), TargetRef: "epic/abc"})
	if err != nil || res.Status != Moved {
		t.Fatalf("moved pin: got %v %v", res, err)
	}
}

func TestMergeConflict(t *testing.T) {
	ctx := context.Background()
	l := newLab(t)
	heads := l.seed("feat")
	l.git("", "-C", l.remote, "branch", "epic/abc", "main")

	// Advance the target so it conflicts with the child.
	work := filepath.Join(t.TempDir(), "w")
	l.git("", "clone", "-q", l.remote, work)
	l.git(work, "checkout", "-q", "epic/abc")
	os.WriteFile(filepath.Join(work, "feat.txt"), []byte("other side\n"), 0o644)
	l.git(work, "add", ".")
	l.git(work, "commit", "-qm", "conflicting")
	l.git(work, "push", "-q", l.remote, "epic/abc")
	// The child must touch the same file: rewrite it on the branch.
	l.git(work, "checkout", "-q", "feat")
	os.WriteFile(filepath.Join(work, "feat.txt"), []byte("child side\n"), 0o644)
	l.git(work, "add", ".")
	l.git(work, "commit", "-qm", "child side")
	l.git(work, "push", "-q", l.remote, "feat")
	head := l.git(work, "rev-parse", "HEAD")
	_ = heads

	res, err := (Git{}).MergeIntoTarget(ctx, Merge{Mirror: l.mirror, Remote: l.remote,
		Branch: "feat", HeadSHA: head, TargetRef: "epic/abc"})
	if err != nil || res.Status != Conflict {
		t.Fatalf("conflict: got %v %v", res, err)
	}
	if !strings.Contains(res.Detail, "feat.txt") {
		t.Fatalf("conflict detail %q names no file", res.Detail)
	}
	// The target is untouched.
	if got := l.remoteSHA("epic/abc"); got == res.SHA {
		t.Fatalf("target moved on a conflicted merge")
	}
}

func TestConcurrentMerges(t *testing.T) {
	t.Run("shared-mirror", func(t *testing.T) { testConcurrentMerges(t, true) })
	t.Run("separate-shems", func(t *testing.T) { testConcurrentMerges(t, false) })
}

func testConcurrentMerges(t *testing.T, sharedMirror bool) {
	ctx := context.Background()
	l := newLab(t)
	branches := make([]string, 0, 4)
	for i := 0; i < 4; i++ {
		branches = append(branches, fmt.Sprintf("feat-%d", i))
	}
	heads := l.seed(branches...)
	l.git("", "-C", l.remote, "branch", "epic/abc", "main")

	// No slot here: all four hit the same target at once. The mirror mutex
	// and the push compare-and-swap must still land every one of them.
	g := Git{}
	var wg sync.WaitGroup
	errs := make(chan error, len(branches))
	for _, b := range branches {
		wg.Add(1)
		go func(b string) {
			defer wg.Done()
			mirror := l.mirror
			if !sharedMirror {
				mirror += "-" + b
			}
			res, err := g.MergeIntoTarget(ctx, Merge{Mirror: mirror, Remote: l.remote,
				Branch: b, HeadSHA: heads[b], TargetRef: "epic/abc"})
			if err != nil {
				errs <- fmt.Errorf("%s: %w", b, err)
				return
			}
			if res.Status != Merged && res.Status != Contained {
				errs <- fmt.Errorf("%s: unexpected status %q (%s)", b, res.Status, res.Detail)
			}
		}(b)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	// Every branch head is an ancestor of the final target.
	final := l.remoteSHA("epic/abc")
	for _, b := range branches {
		if err := exec.Command("git", "-C", l.remote, "merge-base", "--is-ancestor", heads[b], final).Run(); err != nil {
			t.Errorf("%s (%s) is not contained in the final target", b, heads[b])
		}
	}
}

// The head the gate pinned is not the head the remote holds anymore: the
// merge must refuse rather than integrate unreviewed commits. The caller
// re-pins and re-runs the lane.
func TestMergeMovedPin(t *testing.T) {
	ctx := context.Background()
	l := newLab(t)
	heads := l.seed("feat")
	l.git("", "-C", l.remote, "branch", "epic/abc", "main")

	// Advance the branch after the pin.
	work := filepath.Join(l.t.TempDir(), "work")
	l.git("", "clone", "-q", l.remote, work)
	l.git(work, "checkout", "-q", "feat")
	os.WriteFile(filepath.Join(work, "more.txt"), []byte("more\n"), 0o644)
	l.git(work, "add", ".")
	l.git(work, "commit", "-qm", "more")
	l.git(work, "push", "-q", "origin", "feat")

	g := Git{}
	res, err := g.MergeIntoTarget(ctx, Merge{Mirror: l.mirror, Remote: l.remote,
		Branch: "feat", HeadSHA: heads["feat"], TargetRef: "epic/abc"})
	if err != nil || res.Status != Moved {
		t.Fatalf("moved pin: got %v %v", res, err)
	}
	// Nothing was merged.
	if l.remoteSHA("epic/abc") != l.remoteSHA("main") {
		t.Fatalf("target moved despite the pin mismatch")
	}
}

// A remote that refuses a checked-out target yields a diagnostic rejection.
func TestMergePushRejected(t *testing.T) {
	l := newLab(t)
	heads := l.seed("feat")
	l.git("", "-C", l.remote, "config", "core.bare", "false")
	l.git("", "-C", l.remote, "symbolic-ref", "HEAD", "refs/heads/epic/abc")
	res, err := (Git{}).MergeIntoTarget(context.Background(), Merge{Mirror: l.mirror, Remote: l.remote, Branch: "feat", HeadSHA: heads["feat"], TargetRef: "epic/abc"})
	if err != nil || res.Status != Rejected || !strings.Contains(res.Detail, "checked out") {
		t.Fatalf("rejected push: %v %v", res, err)
	}
}

// A crash can leave the mirror's worktree and scratch refs behind; the next
// merge cleans them up and proceeds.
func TestMergeCrashDebris(t *testing.T) {
	ctx := context.Background()
	l := newLab(t)
	heads := l.seed("feat")
	l.git("", "-C", l.remote, "branch", "epic/abc", "main")

	g := Git{}
	// First a normal merge so the mirror exists...
	res, err := g.MergeIntoTarget(ctx, Merge{Mirror: l.mirror, Remote: l.remote,
		Branch: "feat", HeadSHA: heads["feat"], TargetRef: "epic/abc"})
	if err != nil || res.Status != Merged {
		t.Fatalf("seed merge: %v %v", res, err)
	}
	// ...then simulate the debris of a killed pass: a registered worktree
	// left behind (the scratch refs from the merge above are still there).
	wt := l.mirror + ".worktree"
	l.git("", "-C", l.mirror, "worktree", "add", "--detach", wt, "refs/golem/forge/target")

	heads2 := l.seed("feat2")
	res, err = g.MergeIntoTarget(ctx, Merge{Mirror: l.mirror, Remote: l.remote,
		Branch: "feat2", HeadSHA: heads2["feat2"], TargetRef: "epic/abc"})
	if err != nil || res.Status != Merged {
		t.Fatalf("merge over debris: %v %v", res, err)
	}
	final := l.remoteSHA("epic/abc")
	if err := exec.Command("git", "-C", l.remote, "merge-base", "--is-ancestor", heads2["feat2"], final).Run(); err != nil {
		t.Fatalf("feat2 not contained after the debris merge")
	}
}

// Local repositories belong to the agent. Neither upload-pack hooks nor
// receive hooks may run in the credential-holding shem process.
func TestLocalTransportDoesNotRunAgentHooks(t *testing.T) {
	for _, scheme := range []string{"", "file://"} {
		t.Run(scheme, func(t *testing.T) {
			l := newLab(t)
			heads := l.seed("feat")
			evidence := filepath.Join(t.TempDir(), "hook-ran")
			hook := "#!/bin/sh\nprintf '%s' \"$GOLEM_PUSH_TOKEN\" > '" + evidence + "'\n"
			if err := os.WriteFile(filepath.Join(l.remote, "hooks", "pre-receive"), []byte(hook), 0755); err != nil {
				t.Fatal(err)
			}
			packHook := filepath.Join(t.TempDir(), "pack-hook")
			if err := os.WriteFile(packHook, []byte(hook+"exec \"$@\"\n"), 0755); err != nil {
				t.Fatal(err)
			}
			l.git("", "-C", l.remote, "config", "uploadpack.packObjectsHook", packHook)
			t.Setenv("GOLEM_PUSH_TOKEN", "dummy-push-credential")
			t.Setenv("GOLEM_GITHUB_TOKEN", "dummy-github-credential")
			res, err := (Git{}).MergeIntoTarget(context.Background(), Merge{Mirror: l.mirror, Remote: scheme + l.remote, Branch: "feat", HeadSHA: heads["feat"], TargetRef: "epic/test"})
			if err != nil || res.Status != Merged {
				t.Fatalf("safe merge failed: %v %v", res, err)
			}
			if _, err := os.Stat(evidence); !os.IsNotExist(err) {
				t.Fatal("agent-controlled hook ran during local integration")
			}
		})
	}
}

// ls-remote must not discover configuration in the daemon's cwd. Otherwise
// an agent-authored URL rewrite could execute a transport with credentials.
func TestHeadDoesNotReadWorkingDirectoryConfig(t *testing.T) {
	l := newLab(t)
	evidence := filepath.Join(t.TempDir(), "transport-ran")
	l.git("", "-C", l.remote, "config", "protocol.ext.allow", "always")
	l.git("", "-C", l.remote, "config", "url.ext::sh -c 'touch "+evidence+"'.insteadOf", "golem-test://repo")
	t.Chdir(l.remote)
	_, _ = (Git{}).HeadSHA(context.Background(), "golem-test://repo", "feat")
	if _, err := os.Stat(evidence); !os.IsNotExist(err) {
		t.Fatal("ls-remote executed cwd-controlled transport")
	}
}

func TestLocalAlternateRefsDoesNotExecuteAgentConfig(t *testing.T) {
	l := newLab(t)
	heads := l.seed("feat")
	alt := newLab(t)
	alt.seed("alt")
	evidence := filepath.Join(t.TempDir(), "ran")
	hook := filepath.Join(t.TempDir(), "alternate-command")
	if err := os.WriteFile(hook, []byte("#!/bin/sh\ntouch '"+evidence+"'\n"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(l.remote, "objects", "info", "alternates"), []byte(filepath.Join(alt.remote, "objects")+"\n"), 0644); err != nil {
		t.Fatal(err)
	}
	l.git("", "-C", l.remote, "config", "core.alternateRefsCommand", hook)
	_, err := (Git{}).MergeIntoTarget(context.Background(), Merge{Mirror: l.mirror, Remote: l.remote, Branch: "feat", HeadSHA: heads["feat"], TargetRef: "epic/a"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(evidence); !os.IsNotExist(err) {
		t.Fatal("agent-owned alternateRefsCommand executed")
	}
}

// The deployed shem integrates repositories owned by the unprivileged agent.
// Same-UID fixtures miss Git's ownership checks.
func TestLocalTransportAgentOwnedRepository(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("requires root to create a different-UID repository")
	}
	for _, scheme := range []string{"", "file://"} {
		t.Run(scheme, func(t *testing.T) {
			l := newLab(t)
			quotedRemote := filepath.Join(filepath.Dir(l.remote), "agent's repo $(echo injected).git")
			if err := os.Rename(l.remote, quotedRemote); err != nil {
				t.Fatal(err)
			}
			l.remote = quotedRemote
			heads := l.seed("feat")
			if err := filepath.WalkDir(l.remote, func(path string, _ os.DirEntry, err error) error {
				if err != nil {
					return err
				}
				return os.Chown(path, 65534, 65534)
			}); err != nil {
				t.Fatal(err)
			}
			res, err := (Git{}).MergeIntoTarget(context.Background(), Merge{Mirror: l.mirror, Remote: scheme + l.remote, Branch: "feat", HeadSHA: heads["feat"], TargetRef: "epic/agent-owned"})
			if err != nil || res.Status != Merged {
				t.Fatalf("agent-owned merge failed: %v %v", res, err)
			}
		})
	}
}
