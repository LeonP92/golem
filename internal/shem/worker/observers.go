package worker

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/leonp92/golem/internal/shem/client"
)

// observerRoles are the watchers run over a finished work phase. A
// repository opts out of one by deleting its role file.
var observerRoles = []string{"convention-enforcer", "spec-adherence"}

// observe runs the observers before the review gate so the reviewer sees
// their findings. Best effort, like the gate: an observer that cannot run is
// reported, not fatal to the ticket.
func (e *GolemExecutor) observe(ctx context.Context, c *client.Client, repoPath, ticketDir, ticketID, baseBranch, model string) {
	if err := e.runObservers(ctx, repoPath, filepath.Join(ticketDir, "worktree"), ticketID, baseBranch, model); err != nil {
		postStatus(c, ticketID, "Observers failed to run: "+firstLineOf(err.Error()))
		log.Printf("executor: observers for %s: %v", ticketID, err)
	}
}

// runObservers dispatches every observer role over the ticket's work so far,
// for the reviewer to read in the log.
//
// Run by the SHEM, for the reason runGolemReview is: Claude Code strips
// CLAUDE_CODE_OAUTH_TOKEN from the agent's shell, so an observer started
// from there has no model credential and reports "Not logged in". asAgent
// keeps that credential and drops the push token.
//
// With a base branch the review covers the whole branch, not only its last
// commit — a ticket is several commits, and the rest went unseen.
func (e *GolemExecutor) runObservers(ctx context.Context, repoPath, worktree, ticketID, baseBranch, model string) error {
	head, err := worktreeHead(ctx, worktree)
	if err != nil {
		return fmt.Errorf("observers: resolving the commit to review: %w", err)
	}
	var errs []error
	for _, role := range observerRoles {
		if _, err := os.Stat(filepath.Join(repoPath, ".golem", "roles", role+".md")); err != nil {
			continue
		}
		args := []string{"observer", "dispatch", "--ticket", ticketID, "--role", role, "--commit", head}
		if baseBranch != "" {
			args = append(args, "--base", "origin/"+baseBranch)
		}
		args = e.Agent.subcommandArgs(args, model)
		cmd := asAgent(exec.CommandContext(ctx, "golem", args...))
		cmd.Dir = repoPath
		if out, err := cmd.CombinedOutput(); err != nil {
			errs = append(errs, fmt.Errorf("observer %s: %w\n%s", role, err, out))
		}
	}
	return errors.Join(errs...)
}

// worktreeHead resolves HEAD rather than passing "HEAD" along, because the
// observer records the commit it reviewed and skips one it has seen: a
// literal "HEAD" would make every later revision look already reviewed. As the
// agent, since root is refused by git's ownership check on this checkout.
func worktreeHead(ctx context.Context, worktree string) (string, error) {
	cmd := asAgent(exec.CommandContext(ctx, "git", "rev-parse", "HEAD"))
	cmd.Dir = worktree
	out, err := cmd.Output()
	return strings.TrimSpace(string(out)), err
}
