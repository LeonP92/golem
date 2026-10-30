package worker

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/leonp92/golem/internal/evidence"
	"github.com/leonp92/golem/internal/forge"
	"github.com/leonp92/golem/internal/shem/client"
)

// migrateRetryAfter spaces out attempts per ticket, so a failing push is not
// repeated on every poll.
const migrateRetryAfter = 5 * time.Minute

// migrateWaiting hands tickets that are waiting out this shem's usage limit to
// the pool, so a shem that is not limited can try them instead of the work
// idling until the reset. No model runs here: it is plain git.
//
// For each ticket it first makes the work recoverable from the remote alone:
// unfinished edits (tracked changes and untracked files that .gitignore does
// not exclude) are committed as one capture commit on the ticket branch, and
// the branch is pushed without force, only when the remote tip is an ancestor
// of the commit. Only after the remote is confirmed to hold the commit does it
// ask the server to move the ticket, naming that commit. Every failure leaves
// the ticket waiting here, and a capture commit left by a crash is simply
// pushed on the next attempt. no_push and local:// repositories never move.
// The server decides eligibility (automatic, unpinned, not moved before) and
// keeps the backend fixed.
func (w *Worker) migrateWaiting() {
	if w.cfg.NoPush {
		return
	}
	tickets, err := w.client.Migratable()
	if err != nil {
		log.Printf("worker: migratable: %v", err)
		return
	}
	for _, t := range tickets {
		if strings.HasPrefix(t.RepoRemote, "local://") {
			continue
		}
		repoPath := repoLocalPath(w.cfg, t.RepoRemote)
		if repoPath == "" || !w.migrateDue(t.TicketID) {
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		captured, err := w.preserve(ctx, repoPath, t)
		cancel()
		if err != nil {
			log.Printf("worker: ticket %s stays on this shem: %v", t.TicketID, err)
			continue
		}
		if captured == t.CheckpointSHA {
			captured = ""
		}
		if err := w.client.MigrateTicket(t.TicketID, t.CheckpointSHA, captured); err != nil {
			log.Printf("worker: migrate %s: %v", t.TicketID, err)
			continue
		}
		log.Printf("worker: ticket %s moved to the pool while backend %q is limited", t.TicketID, w.backend())
	}
}

func (w *Worker) migrateDue(id string) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.migrateTried == nil {
		w.migrateTried = map[string]time.Time{}
	}
	if last, ok := w.migrateTried[id]; ok && time.Since(last) < migrateRetryAfter {
		return false
	}
	w.migrateTried[id] = time.Now()
	return true
}

// preserve returns the commit the next shem should resume from, after making
// sure the remote has it.
func (w *Worker) preserve(ctx context.Context, repoPath string, t client.MigratableTicket) (string, error) {
	worktree := filepath.Join(repoPath, ".golem", "tickets", t.TicketID, "worktree")
	w.mu.Lock()
	_, busy := w.running[t.TicketID]
	w.mu.Unlock()
	if busy {
		return "", fmt.Errorf("an agent is running in the worktree")
	}
	head, err := worktreeHead(ctx, worktree)
	if err != nil {
		return "", fmt.Errorf("no worktree: %w", err)
	}
	if head != t.CheckpointSHA && gitRun(ctx, worktree, "merge-base", "--is-ancestor", t.CheckpointSHA, head) != nil {
		return "", fmt.Errorf("worktree HEAD is not on the checkpoint's history")
	}
	status := asAgent(exec.CommandContext(ctx, "git", "status", "--porcelain", "--untracked-files=all")) //nolint:gosec
	status.Dir = worktree
	out, err := status.Output()
	if err != nil {
		return "", fmt.Errorf("git status: %w", err)
	}
	// Scan from the ticket's pinned base, not the checkpoint: commits before the
	// checkpoint may be local-only, and this push is what publishes them.
	scanBase := t.CheckpointSHA
	if b, err := loadDiagnosisBase(filepath.Dir(worktree)); err == nil && b != "" {
		scanBase = b
	}
	dirty := len(strings.TrimSpace(string(out))) != 0
	if dirty {
		if head, err = w.capture(ctx, worktree, scanBase); err != nil {
			return "", err
		}
	}
	if head == t.CheckpointSHA && w.remoteHas(ctx, repoPath, t.RepoRemote, t.Branch, head) {
		return head, nil
	}
	if !dirty {
		if err := w.scan(ctx, worktree, nil, scanBase, false); err != nil {
			return "", err
		}
	}
	if w.afterCapture != nil {
		w.afterCapture()
	}
	// The ticket may have been stopped, retried, pinned or moved while
	// the capture ran. Ask the server again right before anything leaves
	// this machine; the migrate call then fences the same facts.
	if !w.stillMigratable(t) {
		return "", fmt.Errorf("the ticket is no longer eligible to move; not pushing")
	}
	if err := w.pushCapture(ctx, repoPath, worktree, t.Branch, t.RepoRemote); err != nil {
		return "", err
	}
	if !w.remoteHas(ctx, repoPath, t.RepoRemote, t.Branch, head) {
		return "", fmt.Errorf("the remote does not hold %s after the push", head)
	}
	return head, nil
}

func (w *Worker) stillMigratable(t client.MigratableTicket) bool {
	again, err := w.client.Migratable()
	if err != nil {
		return false
	}
	for _, a := range again {
		if a.TicketID == t.TicketID && a.CheckpointSHA == t.CheckpointSHA {
			return true
		}
	}
	return false
}

// capture commits everything in the worktree that git does not ignore as one
// commit, without hooks, and returns the new HEAD. It builds the commit in a
// private index, so a refusal leaves the working tree and the user's own
// staging exactly as they were. It refuses a sensitive path (the same list the
// evidence package uses for checkouts), any added line that evidence.Clean
// would redact (credential assignments, private keys), and binary files,
// which cannot be scanned.
func (w *Worker) capture(ctx context.Context, worktree, base string) (string, error) {
	gitDir, err := w.gitOut(ctx, worktree, nil, "rev-parse", "--absolute-git-dir")
	if err != nil {
		return "", fmt.Errorf("capture: %w", err)
	}
	index := filepath.Join(strings.TrimSpace(gitDir), "golem-capture-index")
	_ = os.Remove(index)
	defer func() { _ = os.Remove(index) }()
	env := []string{"GIT_INDEX_FILE=" + index,
		"GIT_AUTHOR_NAME=golem", "GIT_AUTHOR_EMAIL=golem@localhost",
		"GIT_COMMITTER_NAME=golem", "GIT_COMMITTER_EMAIL=golem@localhost"}
	run := func(args ...string) (string, error) { return w.gitOut(ctx, worktree, env, args...) }
	if _, err := run("read-tree", "HEAD"); err != nil {
		return "", fmt.Errorf("capture: %w", err)
	}
	if _, err := run("add", "-A"); err != nil {
		return "", fmt.Errorf("capture: %w", err)
	}
	if err := w.scan(ctx, worktree, env, base, true); err != nil {
		return "", err
	}
	tree, err := run("write-tree")
	if err != nil {
		return "", fmt.Errorf("capture: %w", err)
	}
	head, err := worktreeHead(ctx, worktree)
	if err != nil {
		return "", err
	}
	commit, err := run("commit-tree", strings.TrimSpace(tree), "-p", head, "-m", "golem: capture unfinished work (usage limit)")
	if err != nil {
		return "", fmt.Errorf("capture: %w", err)
	}
	commit = strings.TrimSpace(commit)
	if _, err := run("update-ref", "-m", "golem capture", "HEAD", commit, head); err != nil {
		return "", fmt.Errorf("capture: %w", err)
	}
	// The worktree already matches the commit; point the real index at it.
	if _, err := w.gitOut(ctx, worktree, nil, "reset", "-q"); err != nil {
		return "", fmt.Errorf("capture: %w", err)
	}
	return commit, nil
}

// scan refuses to publish what changed since base (the ticket's pinned base, else the checkpoint, which
// the remote may not hold) when it touches a sensitive path (the evidence
// package's list), adds a line evidence.Clean would redact (credential
// assignments, private keys), or is binary and so cannot be scanned. cached
// compares the capture index; otherwise the worktree HEAD.
func (w *Worker) scan(ctx context.Context, worktree string, env []string, base string, cached bool) error {
	args := func(extra ...string) []string {
		a := []string{"diff"}
		if cached {
			a = append(a, "--cached")
		}
		a = append(a, extra...)
		if cached {
			return append(a, base)
		}
		return append(a, base, "HEAD")
	}
	names, err := w.gitOut(ctx, worktree, env, args("--numstat", "-z")...)
	if err != nil {
		return fmt.Errorf("scan: %w", err)
	}
	for _, rec := range strings.Split(names, "\x00") {
		f := strings.SplitN(rec, "\t", 3)
		if len(f) < 3 {
			continue
		}
		if f[0] == "-" {
			return fmt.Errorf("refused: %q is binary and cannot be scanned for secrets", f[2])
		}
		if evidence.SensitivePath(f[2]) {
			return fmt.Errorf("refused: %q is a sensitive path", f[2])
		}
	}
	diff, err := w.gitOut(ctx, worktree, env, args("--unified=0")...)
	if err != nil {
		return fmt.Errorf("scan: %w", err)
	}
	if evidence.Clean(diff) != diff {
		return fmt.Errorf("refused: the changes contain what looks like a credential or private key")
	}
	return nil
}

func (w *Worker) gitOut(ctx context.Context, dir string, env []string, args ...string) (string, error) {
	c := asAgent(exec.CommandContext(ctx, "git", args...)) //nolint:gosec
	c.Dir = dir
	// asAgent set the credential-free environment; only add to it.
	c.Env = append(c.Env, env...)
	out, err := c.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git %s: %w\n%s", args[0], err, out)
	}
	return string(out), nil
}

// pushCapture publishes the worktree HEAD to the ticket branch the way
// pushTicketBranch does (token-free fetch into the Golem-owned mirror, then a
// push from the mirror), with two differences: it never forces, and it
// refuses when the remote branch has commits the worktree does not contain.
func (w *Worker) pushCapture(ctx context.Context, repoPath, worktree, branch, remote string) error {
	mirror := pushMirrorPath(repoPath)
	if err := ensurePushMirror(ctx, mirror, remote); err != nil {
		return err
	}
	mu := forge.MirrorLock(mirror)
	mu.Lock()
	defer mu.Unlock()
	run := func(env []string, args ...string) (string, error) {
		c := exec.CommandContext(ctx, "git", append([]string{"-C", mirror}, args...)...) //nolint:gosec
		if env != nil {
			c.Env = env
		}
		out, err := c.CombinedOutput()
		return string(out), err
	}
	_, _ = run(nil, "update-ref", "-d", "refs/golem/remote-tip")
	// A missing remote branch is fine; any other fetch failure is not.
	if out, err := run(nil, "fetch", "--no-tags", remote, "+refs/heads/"+branch+":refs/golem/remote-tip"); err != nil &&
		!strings.Contains(out, "couldn't find remote ref") {
		return fmt.Errorf("checking the remote branch: %w\n%s", err, out)
	}
	if out, err := run(withoutPushCredential(os.Environ()), "fetch", "--no-tags", worktree, "+HEAD:refs/golem/capture"); err != nil {
		return fmt.Errorf("git fetch from the worktree: %w\n%s", err, out)
	}
	if _, err := run(nil, "rev-parse", "--verify", "-q", "refs/golem/remote-tip"); err == nil {
		if out, err := run(nil, "merge-base", "--is-ancestor", "refs/golem/remote-tip", "refs/golem/capture"); err != nil {
			return fmt.Errorf("the remote branch has commits this worktree lacks; not pushing: %w\n%s", err, out)
		}
	}
	if out, err := run(nil, "push", remote, "refs/golem/capture:refs/heads/"+branch); err != nil {
		return fmt.Errorf("git push %s: %w\n%s", branch, err, out)
	}
	return nil
}

// remoteHasCheckpoint fetches the ticket branch from the remote and reports
// whether sha is on it. A missing branch or a failed fetch is "no".
func remoteHasCheckpoint(ctx context.Context, repoPath, remote, branch, sha string) bool {
	if err := fetchBranchForAgent(ctx, repoPath, remote, branch); err != nil {
		return false
	}
	return gitRun(ctx, repoPath, "merge-base", "--is-ancestor", sha, "refs/remotes/origin/"+branch) == nil
}
