// Package forge merges a finished branch into a target ref on any git
// remote. It is the shem-side half of epic integration: plain git commands
// in a Golem-owned mirror, no forge vendor involved.
//
// Forge is an interface so the worker depends on the operation, not on the
// git CLI; Git is the only implementation today, by design — plain git
// speaks to every remote already.
package forge

import (
	"bytes"
	"context"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	"github.com/leonp92/golem/internal/agentenv"
)

// Status is the outcome of one MergeIntoTarget call. Operational failures
// (the git binary, the network, a missing mirror) come back as errors
// instead: they are retried later, never reported as verdicts.
type Status string

const (
	// Merged: a merge commit was created and pushed to the target ref.
	Merged Status = "merged"
	// Contained: the head was already reachable from the target — a merge
	// that already happened (a retry after a crash), so only the receipt
	// is owed.
	Contained Status = "contained"
	// Conflict: the branch does not merge cleanly; Detail names the
	// conflicted files.
	Conflict Status = "conflict"
	// Moved: the remote branch head no longer equals Merge.HeadSHA — the
	// pin this merge was gated on is stale.
	Moved Status = "moved"
	// Rejected: the remote refused the push (permissions, protected
	// branch, a hook). Detail carries the remote's own words.
	Rejected Status = "rejected"
)

// Result reports what MergeIntoTarget did. SHA is the resulting target
// head for Merged and the existing target head for Contained.
type Result struct {
	Status Status
	SHA    string
	Detail string
}

// Merge pins one integration: Branch at HeadSHA into TargetRef on Remote,
// executed inside Mirror (a bare, Golem-owned repository, created if
// missing). MaxAttempts bounds compare-and-swap retries when the target
// moves under the push; zero means the default.
type Merge struct {
	Mirror      string
	Remote      string
	Branch      string
	HeadSHA     string
	TargetRef   string
	Message     string
	MaxAttempts int
}

// defaultAttempts covers the realistic race — a handful of children
// finishing together — without spinning.
const defaultAttempts = 4

// Forge is the integration surface the shem worker depends on.
type Forge interface {
	// HeadSHA returns the remote's current head for branch, or "" when
	// the branch does not exist there.
	HeadSHA(ctx context.Context, remote, branch string) (string, error)
	// MergeIntoTarget integrates one branch into one target ref.
	MergeIntoTarget(ctx context.Context, op Merge) (Result, error)
}

// Git is the plain-git Forge. The zero value is ready to use.
type Git struct{}

// mirrors serializes work per mirror path: two integrations into different
// refs of the same repository still share the mirror's object store and
// worktree admin, which git does not make safe to drive concurrently.
var mirrors sync.Map // string -> *sync.Mutex

func mirrorLock(path string) *sync.Mutex {
	mu, _ := mirrors.LoadOrStore(filepath.Clean(path), &sync.Mutex{})
	return mu.(*sync.Mutex)
}

// MirrorLock shares the merge lane's mutex with worker mirror initialization
// and base-ref creation, which use the same bare repository.
func MirrorLock(path string) *sync.Mutex { return mirrorLock(path) }

// HeadSHA implements Forge; ls-remote only talks to the remote, so no
// local repository is needed.
func (Git) HeadSHA(ctx context.Context, remote, branch string) (string, error) {
	out, err := remoteGit(ctx, "", remote, "ls-remote", remote, "refs/heads/"+branch)
	if err != nil {
		return "", fmt.Errorf("ls-remote %s: %w", branch, err)
	}
	fields := strings.Fields(out)
	if len(fields) == 0 {
		return "", nil
	}
	return fields[0], nil
}

// MergeIntoTarget implements Forge.
func (Git) MergeIntoTarget(ctx context.Context, op Merge) (Result, error) {
	if op.Remote == "" || op.Branch == "" || op.HeadSHA == "" || op.TargetRef == "" {
		return Result{}, fmt.Errorf("forge: remote, branch, head SHA and target ref are all required")
	}
	mu := mirrorLock(op.Mirror)
	mu.Lock()
	defer mu.Unlock()
	if err := ensureMirror(ctx, op.Mirror); err != nil {
		return Result{}, err
	}

	attempts := op.MaxAttempts
	if attempts <= 0 {
		attempts = defaultAttempts
	}
	for attempt := 1; ; attempt++ {
		res, retriable, err := mergeOnce(ctx, op)
		if err != nil {
			return Result{}, err
		}
		if !retriable {
			return res, nil
		}
		if attempt == attempts {
			return Result{}, fmt.Errorf("target kept changing after %d attempts: %s", attempts, res.Detail)
		}
	}
}

// mergeOnce is one fetch-merge-push pass. retriable marks the one outcome
// worth another pass: the push lost the compare-and-swap because the target
// moved, in which case the next pass merges onto the new target head.
func mergeOnce(ctx context.Context, op Merge) (res Result, retriable bool, err error) {
	const headRef = "refs/golem/forge/head"
	const targetRef = "refs/golem/forge/target"
	fetchSpec := fmt.Sprintf("+refs/heads/%s:%s", op.Branch, headRef)
	targetSpec := fmt.Sprintf("+refs/heads/%s:%s", op.TargetRef, targetRef)
	if out, ferr := remoteGit(ctx, op.Mirror, op.Remote, "fetch", "--no-tags", op.Remote, fetchSpec); ferr != nil {
		return Result{}, false, fmt.Errorf("fetching %s: %w\n%s", op.Branch, ferr, out)
	}
	head, err := git(ctx, op.Mirror, "rev-parse", "--verify", headRef)
	if err != nil {
		return Result{}, false, fmt.Errorf("branch %s is not on the remote: %w", op.Branch, err)
	}
	if head != op.HeadSHA {
		return Result{Status: Moved, Detail: fmt.Sprintf("remote head is %s, gated on %s", head, op.HeadSHA)}, false, nil
	}
	// The target is fetched apart from the branch, with the stale mirror
	// ref dropped first: the run's first child merges before the target
	// ref exists anywhere, and one fetch naming both refs would fail
	// outright on the missing one. A missing target is created at the
	// pinned head — the first child's branch IS base plus its commits, so
	// the ref starts with exactly the right content, and the create races
	// a sibling's create through the same CAS retry as a moved target.
	_ = run(ctx, op.Mirror, "update-ref", "-d", targetRef)
	target := ""
	if out, ferr := remoteGit(ctx, op.Mirror, op.Remote, "fetch", "--no-tags", op.Remote, targetSpec); ferr != nil {
		if !strings.Contains(out, "couldn't find remote ref") {
			return Result{}, false, fmt.Errorf("fetching target %s: %w\n%s", op.TargetRef, ferr, out)
		}
	} else if t, terr := git(ctx, op.Mirror, "rev-parse", "--verify", targetRef); terr == nil {
		target = t
	}
	if target == "" {
		out, perr := remoteGit(ctx, op.Mirror, op.Remote, "push", "--force-with-lease=refs/heads/"+op.TargetRef+":", op.Remote, head+":refs/heads/"+op.TargetRef)
		if perr == nil {
			return Result{Status: Merged, SHA: head, Detail: "created " + op.TargetRef}, false, nil
		}
		if targetChanged(out) || strings.Contains(out, "already exists") {
			return Result{Status: Rejected, Detail: out}, true, nil
		}
		return Result{Status: Rejected, Detail: out}, false, nil
	}
	if err := run(ctx, op.Mirror, "merge-base", "--is-ancestor", head, target); err == nil {
		return Result{Status: Contained, SHA: target}, false, nil
	}

	worktree := op.Mirror + ".worktree"
	_ = run(ctx, op.Mirror, "worktree", "remove", "--force", worktree) // leftover from a crash
	if out, werr := git(ctx, op.Mirror, "worktree", "add", "--detach", worktree, target); werr != nil {
		return Result{}, false, fmt.Errorf("worktree for the merge: %w\n%s", werr, out)
	}
	defer func() { _ = run(ctx, op.Mirror, "worktree", "remove", "--force", worktree) }()

	msg := op.Message
	if msg == "" {
		msg = fmt.Sprintf("Merge %s into %s", op.Branch, op.TargetRef)
	}
	_, merr := git(ctx, worktree,
		"-c", "user.name=golem", "-c", "user.email=golem@localhost",
		"merge", "--no-ff", "-m", msg, head)
	if merr != nil {
		files, _ := git(ctx, worktree, "diff", "--name-only", "--diff-filter=U")
		_ = run(ctx, worktree, "merge", "--abort")
		if files == "" {
			return Result{}, false, fmt.Errorf("merging %s: %w", op.Branch, merr)
		}
		return Result{Status: Conflict, Detail: files}, false, nil
	}
	merged, err := git(ctx, worktree, "rev-parse", "HEAD")
	if err != nil {
		return Result{}, false, fmt.Errorf("reading the merge commit: %w", err)
	}

	out, perr := remoteGit(ctx, op.Mirror, op.Remote, "push", "--force-with-lease=refs/heads/"+op.TargetRef+":"+target, op.Remote, merged+":refs/heads/"+op.TargetRef)
	if perr == nil {
		return Result{Status: Merged, SHA: merged}, false, nil
	}
	// A non-fast-forward answer is the compare-and-swap losing: somebody
	// else's merge landed first. Anything else is the remote saying no.
	if targetChanged(out) {
		return Result{Status: Rejected, Detail: out}, true, nil
	}
	return Result{Status: Rejected, Detail: out}, false, nil
}

// Git can lose the CAS either before send-pack (stale info) or inside
// receive-pack's ref transaction (cannot lock ref). Both are contention.
func targetChanged(out string) bool {
	return strings.Contains(out, "non-fast-forward") || strings.Contains(out, "fetch first") || strings.Contains(out, "stale info") || strings.Contains(out, "cannot lock ref")
}

// ensureMirror creates the bare mirror when it is not there yet — the same
// shape as the worker's push mirror: no hooks, no configured remote, so
// nothing about where it fetches from or pushes to can be edited on disk.
// Callers hold the mirror's lock.
func ensureMirror(ctx context.Context, mirror string) error {
	if mirror == "" {
		return fmt.Errorf("forge: no mirror path")
	}
	if _, err := os.Stat(filepath.Join(mirror, "HEAD")); err == nil {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(mirror), 0o750); err != nil {
		return fmt.Errorf("forge mirror: %w", err)
	}
	if out, err := git(ctx, "", "init", "--bare", "--template=", mirror); err != nil {
		return fmt.Errorf("forge mirror: git init --bare: %w\n%s", err, out)
	}
	return nil
}

// remoteGit keeps agent-owned local repositories out of the privileged Git
// execution boundary. Local transport helpers inherit these command-level
// overrides, which take precedence over the repository's own configuration.
func remoteGit(ctx context.Context, dir, remote string, args ...string) (string, error) {
	local := filepath.IsAbs(remote) || strings.HasPrefix(remote, "file://") || !strings.Contains(remote, ":")
	if !local {
		return git(ctx, dir, args...)
	}
	// The shem owns the mirror, but the agent owns the local repository.
	// Trust only that repository inside the credential-free transport helper.
	path := remote
	if strings.HasPrefix(remote, "file://") {
		u, err := url.Parse(remote)
		if err != nil {
			return "", err
		}
		path = u.Path
		if u.Host != "" && u.Host != "localhost" {
			path = "//" + u.Host + path
		}
	}
	path, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	path, err = filepath.EvalSymlinks(path)
	if err != nil {
		return "", err
	}
	// Git runs transport helpers through a shell. Single-quote paths so even
	// repository names containing spaces or shell syntax stay literal data.
	quotedPath := strings.ReplaceAll(filepath.ToSlash(path), "'", "'\\''")
	quotedGitDir := strings.ReplaceAll(filepath.ToSlash(filepath.Join(path, ".git")), "'", "'\\''")
	trust := " -c 'safe.directory=" + quotedPath + "' -c 'safe.directory=" + quotedGitDir + "'"
	helper := "--upload-pack=git" + trust + " -c core.hooksPath=/dev/null -c core.alternateRefsCommand=true -c uploadpack.packObjectsHook=env upload-pack"
	if args[0] == "push" {
		helper = "--receive-pack=git" + trust + " -c core.hooksPath=/dev/null -c core.alternateRefsCommand=true receive-pack"
	}
	args = append([]string{args[0], helper}, args[1:]...)
	return gitEnv(ctx, dir, agentenv.Environ(), args...)
}

// git returns stdout only: diagnostics must never become a parsed SHA.
func git(ctx context.Context, dir string, args ...string) (string, error) {
	return gitEnv(ctx, dir, nil, args...)
}

func gitEnv(ctx context.Context, dir string, env []string, args ...string) (string, error) {
	// Repository discovery must not read the daemon's working directory,
	// which may itself be an agent-owned checkout. Keep no-repo commands in
	// a fresh directory and prevent discovery in its shared parent.
	if dir == "" {
		isolated, err := os.MkdirTemp("", "golem-forge-")
		if err != nil {
			return "", err
		}
		defer func() { _ = os.Remove(isolated) }()
		dir = isolated
		if env == nil {
			env = os.Environ()
		}
		env = append(env, "GIT_CEILING_DIRECTORIES="+filepath.Dir(isolated))
	}
	args = append([]string{"-c", "core.hooksPath=/dev/null", "-c", "core.fsmonitor=false"}, args...)
	cmd := exec.CommandContext(ctx, "git", args...)
	if env == nil {
		env = os.Environ()
	}
	cmd.Dir, cmd.Env = dir, append(env, "LC_ALL=C")
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		out := stdout.String() + stderr.String()
		return out, fmt.Errorf("git %s: %w\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(stdout.String()), nil
}

// run is git for commands whose output the caller does not need.
func run(ctx context.Context, dir string, args ...string) error {
	_, err := git(ctx, dir, args...)
	return err
}
