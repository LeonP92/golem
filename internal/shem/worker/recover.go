package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/leonp92/golem/internal/agentenv"
	"github.com/leonp92/golem/internal/shem/client"
	"github.com/leonp92/golem/internal/shem/config"
	"github.com/leonp92/golem/internal/ticket"
)

// RecoverTicket ensures the local repo is in the correct state for resuming a
// checkpointed ticket, then reconstructs the ticket's local state files.
// setup is the adapter's WorktreeSetup, run on a newly created worktree.
// Idempotent: safe to call on an already-present worktree.
func RecoverTicket(ctx context.Context, claim *client.ClaimResponse, cfg *config.Config, setup func(string) error) error {
	if claim.CheckpointPhase == nil || claim.CheckpointSHA == nil {
		return fmt.Errorf("RecoverTicket: claim has no checkpoint data")
	}

	repoPath := repoLocalPath(cfg, claim.RepoRemote)
	if repoPath == "" {
		return fmt.Errorf("RecoverTicket: no local path for repo %q", claim.RepoRemote)
	}

	if err := CloneIfMissing(ctx, repoPath, repoCloneRemote(cfg, claim.RepoRemote)); err != nil {
		return fmt.Errorf("RecoverTicket: clone: %w", err)
	}

	ticketDir := filepath.Join(repoPath, ".golem", "tickets", claim.TicketID)
	worktreePath := filepath.Join(ticketDir, "worktree")
	worktreeBranch := claim.Branch

	created := false
	if _, err := os.Stat(worktreePath); os.IsNotExist(err) {
		// Fetch in case the branch was pushed; ignore errors (no_push mode has no remote branch).
		_ = fetchBranchForAgent(ctx, repoPath, claim.RepoRemote, worktreeBranch)
		if err := ensureWorktree(ctx, repoPath, worktreePath, worktreeBranch); err != nil {
			return fmt.Errorf("RecoverTicket: worktree: %w", err)
		}
		created = true
	}

	if sha := safeDeref(claim.CheckpointSHA); sha != "" {
		if err := gitRun(ctx, worktreePath, "reset", "--hard", sha); err != nil {
			return fmt.Errorf("RecoverTicket: reset: %w", err)
		}
	}

	// After the reset, which would otherwise undo it.
	if created {
		if err := setup(worktreePath); err != nil {
			return fmt.Errorf("RecoverTicket: worktree setup: %w", err)
		}
	}

	if err := ReconstructState(ticketDir, worktreePath, claim); err != nil {
		return fmt.Errorf("RecoverTicket: reconstruct state: %w", err)
	}

	return nil
}

// fetchBranchForAgent updates origin/<branch> in the agent's repo without
// handing it the token: root fetches into the push mirror, then the agent
// fetches from a bundle of that branch.
func fetchBranchForAgent(ctx context.Context, repoPath, remote, branch string) error {
	mirror := pushMirrorPath(repoPath)
	if err := ensurePushMirror(ctx, mirror, remote); err != nil {
		return err
	}
	ref := "refs/heads/" + branch
	if out, err := exec.CommandContext(ctx, "git", "-C", mirror, "fetch", "--no-tags", remote, "+"+ref+":"+ref).CombinedOutput(); err != nil {
		return fmt.Errorf("git fetch %s into push mirror: %w\n%s", branch, err, out)
	}

	f, err := os.CreateTemp("", "golem-*.bundle")
	if err != nil {
		return err
	}
	bundle := f.Name()
	_ = f.Close()
	defer func() { _ = os.Remove(bundle) }()
	if out, err := exec.CommandContext(ctx, "git", "-C", mirror, "bundle", "create", bundle, ref).CombinedOutput(); err != nil {
		return fmt.Errorf("git bundle %s: %w\n%s", branch, err, out)
	}
	if acct := agentenv.User(); acct != nil {
		if err := os.Chown(bundle, int(acct.UID), int(acct.GID)); err != nil {
			return err
		}
	}
	return gitRun(ctx, repoPath, "fetch", bundle, "+"+ref+":refs/remotes/origin/"+branch)
}

// ensureWorktree adds a git worktree at worktreePath on branch, preferring the
// local branch (covers no_push mode) and falling back to origin/<branch>.
func ensureWorktree(ctx context.Context, repoPath, worktreePath, branch string) error {
	if gitRun(ctx, repoPath, "rev-parse", "--verify", branch) == nil {
		return gitRun(ctx, repoPath, "worktree", "add", worktreePath, branch)
	}
	if gitRun(ctx, repoPath, "rev-parse", "--verify", "origin/"+branch) == nil {
		return gitRun(ctx, repoPath, "worktree", "add", "--track", "-b", branch, worktreePath, "origin/"+branch)
	}
	return fmt.Errorf("branch %q not found locally or on origin", branch)
}

// blogEntryJSON matches the JSON schema written by blog.Writer (base golem).
type blogEntryJSON struct {
	Role      string    `json:"role"`
	Type      string    `json:"type"`
	Message   string    `json:"message"`
	Timestamp time.Time `json:"timestamp"`
}

// ReconstructState writes state.json and log.jsonl into ticketDir from the
// data carried in the ClaimResponse. The directory is created if absent.
// worktreePath is the expected worktree path for the ticket (used by ticket.Load).
func ReconstructState(ticketDir, worktreePath string, claim *client.ClaimResponse) error {
	if err := os.MkdirAll(ticketDir, 0o755); err != nil {
		return err
	}

	state, err := restoredTicketState(ticketDir, worktreePath, claim)
	if err != nil {
		return err
	}
	if err := state.Save(ticketDir); err != nil {
		return err
	}

	// Write log.jsonl — one JSON blog.Entry line per log entry so blog.ReadAll can parse them.
	f, err := os.Create(filepath.Join(ticketDir, "log.jsonl")) //nolint:gosec
	if err != nil {
		return err
	}
	// Closed explicitly at the end rather than deferred: this file is being
	// written, so a close error means the recovered log is incomplete and
	// the caller must hear about it.
	enc := json.NewEncoder(f)
	for _, e := range claim.LogEntries {
		entry := blogEntryJSON{
			Role:      e.FromRole,
			Type:      e.EntryType,
			Message:   e.Message,
			Timestamp: e.CreatedAt,
		}
		if err := enc.Encode(entry); err != nil {
			_ = f.Close() // the encode error is the one worth reporting
			return err
		}
	}
	return f.Close()
}

// Retain the pinned base and local metadata through a revision or restart.
// On a new host, derive the original base from the fetched branch history.
func restoredTicketState(ticketDir, worktreePath string, claim *client.ClaimResponse) (*ticket.State, error) {
	state, err := ticket.Load(ticketDir)
	if os.IsNotExist(err) {
		state = &ticket.State{}
	} else if err != nil {
		return nil, err
	}
	state.ID, state.Description, state.Phase = claim.TicketID, claim.Description, ticket.Phase(safeDeref(claim.CheckpointPhase))
	state.Branch, state.WorktreePath = claim.Branch, worktreePath
	if claim.Epic != nil {
		state.AcceptanceCommands = append([]string(nil), claim.Epic.AcceptanceCommands...)
		if state.BranchBaseSHA == "" {
			for _, ref := range []string{claim.BaseBranch, claim.Epic.BaseFallback} {
				if ref == "" {
					continue
				}
				for _, candidate := range []string{"origin/" + ref, ref} {
					cmd := asAgent(exec.Command("git", "merge-base", "HEAD", candidate))
					cmd.Dir = worktreePath
					if out, err := cmd.Output(); err == nil {
						state.BranchBaseSHA = strings.TrimSpace(string(out))
						break
					}
				}
				if state.BranchBaseSHA != "" {
					break
				}
			}
			if state.BranchBaseSHA == "" {
				return nil, fmt.Errorf("recovering epic child: no readable comparison base")
			}
		}
	}
	return state, nil
}

// CloneIfMissing clones remote into repoPath when there is no git repository
// there yet. Only http/https/ssh/git URL schemes are permitted, to prevent git
// ext:: injection.
//
// "Missing" means no git repository, not merely no directory. The earlier
// os.IsNotExist check was wrong in a way that produced a baffling failure: a
// directory that exists but is not a repository — an empty volume mount, or
// one where a previous run's `golem init` had already created .golem —
// silently skipped the clone, and the first git command to follow died with
//
//	creating worktree: git worktree add: exit status 128
//	fatal: not a git repository (or any of the parent directories): .git
//
// several steps away from the actual cause.
//
// A directory that already contains something other than a git repository is
// reported rather than cloned into: git clone refuses a non-empty target, and
// working around that with init-plus-fetch would be operating on a directory
// whose contents nobody has explained.
func CloneIfMissing(ctx context.Context, repoPath, remote string) error {
	if isGitRepo(repoPath) {
		return nil
	}
	entries, err := os.ReadDir(repoPath)
	switch {
	case os.IsNotExist(err):
		// Nothing there at all: the ordinary first-clone case.
	case err != nil:
		return fmt.Errorf("inspecting %s: %w", repoPath, err)
	case len(entries) > 0:
		return fmt.Errorf("%s is not a git repository and is not empty "+
			"(contains %d entr%s, e.g. %q) — clone %s there yourself, or empty "+
			"the directory and let golem clone it",
			repoPath, len(entries), plural(len(entries)), entries[0].Name(), remote)
	}
	if !isSafeRemote(remote) {
		return fmt.Errorf("remote URL scheme not allowed: %q", remote)
	}
	return gitRun(ctx, "", "clone", remote, repoPath)
}

func plural(n int) string {
	if n == 1 {
		return "y"
	}
	return "ies"
}

// isGitRepo reports whether repoPath is the root of a git repository. Checks
// for .git rather than running git, so it cannot be confused by a parent
// directory that happens to be a repository — which is exactly what the old
// failure message ("or any of the parent directories") was complaining about.
func isGitRepo(repoPath string) bool {
	_, err := os.Stat(filepath.Join(repoPath, ".git"))
	return err == nil
}

func isSafeRemote(remote string) bool {
	for _, prefix := range []string{"https://", "http://", "ssh://", "git://", "git@"} {
		if len(remote) >= len(prefix) && remote[:len(prefix)] == prefix {
			return true
		}
	}
	return false
}

// gitRun runs a git sub-command in dir (empty string means no Dir override).
// Inside a repo it runs as the agent (asAgent); only the clone, with no dir,
// runs as root.
func gitRun(ctx context.Context, dir string, args ...string) error {
	cmd := exec.CommandContext(ctx, "git", args...) //nolint:gosec
	if dir != "" {
		cmd.Dir = dir
		asAgent(cmd)
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git %v: %w\n%s", args, err, out)
	}
	return nil
}

func safeDeref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
