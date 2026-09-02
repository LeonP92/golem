package workspace

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

func Create(repoRoot, ticketID, branchBaseSHA string) (worktreePath, branch string, err error) {
	branch = "ticket/" + ticketID
	worktreePath = filepath.Join(repoRoot, ".golem", "tickets", ticketID, "worktree")

	cmd := exec.Command("git", "worktree", "add", "-b", branch, worktreePath, branchBaseSHA)
	cmd.Dir = repoRoot
	if out, err := cmd.CombinedOutput(); err != nil {
		return "", "", fmt.Errorf("git worktree add: %w\n%s", err, out)
	}
	return worktreePath, branch, nil
}

func Remove(repoRoot, worktreePath, branch string) error {
	if _, err := os.Stat(worktreePath); err == nil {
		cmd := exec.Command("git", "worktree", "remove", worktreePath, "--force")
		cmd.Dir = repoRoot
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("git worktree remove: %w\n%s", err, out)
		}
	}
	cmd := exec.Command("git", "branch", "-D", branch)
	cmd.Dir = repoRoot
	if out, err := cmd.CombinedOutput(); err != nil {
		// branch already deleted (e.g. merged and cleaned up manually) — not an error
		_ = out
	}
	return nil
}
