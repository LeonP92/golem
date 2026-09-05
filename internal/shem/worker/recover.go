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
//
// Steps:
//  1. Clone repo if not present at cfg.RepoPath (resolved from claim.RepoRemote).
//  2. git fetch origin
//  3. git checkout claim.Branch (creating tracking branch if necessary)
//  4. git reset --hard claim.CheckpointSHA
//  5. Write .golem/tickets/<id>/state.json and log.jsonl from claim data.
//
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

	if err := gitRun(ctx, repoPath, "fetch", "origin"); err != nil {
		return fmt.Errorf("RecoverTicket: fetch: %w", err)
	}

	if err := checkoutBranch(ctx, repoPath, claim.Branch); err != nil {
		return fmt.Errorf("RecoverTicket: checkout: %w", err)
	}

	if err := gitRun(ctx, repoPath, "reset", "--hard", *claim.CheckpointSHA); err != nil {
		return fmt.Errorf("RecoverTicket: reset: %w", err)
	}

	ticketDir := filepath.Join(repoPath, ".golem", "tickets", claim.TicketID)
	worktreePath := filepath.Join(repoPath, ".golem", "tickets", claim.TicketID, "worktree")
	if err := ReconstructState(ticketDir, worktreePath, claim); err != nil {
		return fmt.Errorf("RecoverTicket: reconstruct state: %w", err)
	}

	return nil
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

// checkoutBranch checks out branch, creating a local tracking branch if needed.
func checkoutBranch(ctx context.Context, repoPath, branch string) error {
	// First try a plain checkout (branch already exists locally).
	if err := gitRun(ctx, repoPath, "checkout", branch); err == nil {
		return nil
	}
	// Fall back to creating the branch tracking origin.
	return gitRun(ctx, repoPath, "checkout", "-b", branch, "origin/"+branch)
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
