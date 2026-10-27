package worker

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/leonp92/golem/internal/evidence"
	"github.com/leonp92/golem/internal/shem/config"
	"github.com/leonp92/golem/internal/ticket"
)

// runSetup runs the project bootstrap commands, separate from vendor setup.
func runSetup(ctx context.Context, dir string, commands []string, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	for _, line := range commands {
		cmd := asAgent(exec.CommandContext(ctx, "sh", "-c", line))
		cmd.Dir = dir
		cmd.WaitDelay = time.Second
		out, err := cmd.CombinedOutput()
		if err != nil {
			return fmt.Errorf(evidence.EnvironmentMarker+" worktree setup %q: %w\n%s", line, err, evidence.Clean(string(out)))
		}
	}
	return nil
}

func (e *GolemExecutor) prepareWorktree(ctx context.Context, cfg *config.Config, remote, ticketDir string) error {
	worktree := filepath.Join(ticketDir, "worktree")
	if err := evidence.CheckCheckout(worktree); err != nil {
		return err
	}
	var commands []string
	timeout := 5 * time.Minute
	for _, repo := range cfg.Repos {
		if repo.NormalizedRemote == remote {
			commands = repo.WorktreeSetup
			if repo.SetupTimeout > 0 {
				timeout = repo.SetupTimeout
			}
			break
		}
	}
	if len(commands) == 0 {
		return nil
	}
	// A retry preserves edits; do not key on moving HEAD and re-install every
	// time the implementer commits. Recreated worktrees lose their marker.
	base := ""
	if state, err := ticket.Load(ticketDir); err == nil {
		base = state.BranchBaseSHA
	}
	sum := sha256.Sum256([]byte(strings.Join(commands, "\x00") + "\x00" + base))
	key := hex.EncodeToString(sum[:])
	// The worktree's own git dir keeps the marker out of the checkout and dies with the worktree.
	gitDir := asAgent(exec.CommandContext(ctx, "git", "rev-parse", "--absolute-git-dir"))
	gitDir.Dir = worktree
	out, err := gitDir.Output()
	if err != nil {
		return fmt.Errorf("worktree setup marker: %w", err)
	}
	marker := filepath.Join(strings.TrimSpace(string(out)), "golem-worktree-setup")
	if old, err := os.ReadFile(marker); err == nil && string(old) == key {
		return nil
	}
	if err := runSetup(ctx, worktree, commands, timeout); err != nil {
		return err
	}
	if err := evidence.CheckCheckout(worktree); err != nil {
		return err
	}
	return os.WriteFile(marker, []byte(key), 0644)
}
