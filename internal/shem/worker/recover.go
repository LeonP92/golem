package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/leonp92/golem/internal/shem/client"
	"github.com/leonp92/golem/internal/shem/config"
)

// RecoverTicket ensures the local repo is in the correct state for resuming a
// checkpointed ticket, then reconstructs the ticket's local state files.
// Idempotent: safe to call on an already-present worktree.
func RecoverTicket(ctx context.Context, claim *client.ClaimResponse, cfg *config.Config) error {
	if claim.CheckpointPhase == nil || claim.CheckpointSHA == nil {
		return fmt.Errorf("RecoverTicket: claim has no checkpoint data")
	}

	repoPath := repoLocalPath(cfg, claim.RepoRemote)
	if repoPath == "" {
		return fmt.Errorf("RecoverTicket: no local path for repo %q", claim.RepoRemote)
	}

	if err := CloneIfMissing(ctx, repoPath, claim.RepoRemote); err != nil {
		return fmt.Errorf("RecoverTicket: clone: %w", err)
	}

	ticketDir := filepath.Join(repoPath, ".golem", "tickets", claim.TicketID)
	worktreePath := filepath.Join(ticketDir, "worktree")
	worktreeBranch := "ticket/" + claim.TicketID

	if _, err := os.Stat(worktreePath); os.IsNotExist(err) {
		// Fetch in case the branch was pushed; ignore errors (no_push mode has no remote branch).
		_ = gitRun(ctx, repoPath, "fetch", "origin")
		if err := ensureWorktree(ctx, repoPath, worktreePath, worktreeBranch); err != nil {
			return fmt.Errorf("RecoverTicket: worktree: %w", err)
		}
	}

	if sha := safeDeref(claim.CheckpointSHA); sha != "" {
		if err := gitRun(ctx, worktreePath, "reset", "--hard", sha); err != nil {
			return fmt.Errorf("RecoverTicket: reset: %w", err)
		}
	}

	if err := ReconstructState(ticketDir, worktreePath, claim); err != nil {
		return fmt.Errorf("RecoverTicket: reconstruct state: %w", err)
	}

	return nil
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

// stateFile matches the fields required by ticket.Load (base golem's state.go).
type stateFile struct {
	ID           string `json:"id"`
	Description  string `json:"description"`
	Phase        string `json:"phase"`
	WorktreePath string `json:"worktree_path"`
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

	// Write state.json — must match ticket.State struct (fields: id, description, phase, worktree_path).
	state := stateFile{
		ID:           claim.TicketID,
		Description:  claim.Description,
		Phase:        safeDeref(claim.CheckpointPhase),
		WorktreePath: worktreePath,
	}
	stateData, err := json.Marshal(state)
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(ticketDir, "state.json"), stateData, 0o644); err != nil { //nolint:gosec
		return err
	}

	// Write log.jsonl — one JSON blog.Entry line per log entry so blog.ReadAll can parse them.
	f, err := os.Create(filepath.Join(ticketDir, "log.jsonl")) //nolint:gosec
	if err != nil {
		return err
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	for _, e := range claim.LogEntries {
		entry := blogEntryJSON{
			Role:      e.FromRole,
			Type:      e.EntryType,
			Message:   e.Message,
			Timestamp: e.CreatedAt,
		}
		if err := enc.Encode(entry); err != nil {
			return err
		}
	}
	return nil
}

// CloneIfMissing runs `git clone remote repoPath` if repoPath does not exist.
// CloneIfMissing runs `git clone remote repoPath` if repoPath does not exist.
// Only http/https/ssh/git URL schemes are permitted to prevent git ext:: injection.
func CloneIfMissing(ctx context.Context, repoPath, remote string) error {
	if _, err := os.Stat(repoPath); os.IsNotExist(err) {
		if !isSafeRemote(remote) {
			return fmt.Errorf("remote URL scheme not allowed: %q", remote)
		}
		return gitRun(ctx, "", "clone", remote, repoPath)
	}
	return nil
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
func gitRun(ctx context.Context, dir string, args ...string) error {
	cmd := exec.CommandContext(ctx, "git", args...) //nolint:gosec
	if dir != "" {
		cmd.Dir = dir
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
