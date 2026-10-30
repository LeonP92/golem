package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/leonp92/golem/internal/agentrunner"
	"github.com/leonp92/golem/internal/blog"
	"github.com/leonp92/golem/internal/config"
	"github.com/leonp92/golem/internal/evidence"
	"github.com/leonp92/golem/internal/models"
	"github.com/leonp92/golem/internal/ticket"
)

var knownPhases = map[string]ticket.Phase{
	"brainstorm":       ticket.PhaseBrainstorm,
	"plan":             ticket.PhasePlan,
	"implement":        ticket.PhaseImplement,
	"review":           ticket.PhaseReview,
	"ready-for-review": ticket.PhaseReadyForReview,
	"needs-attention":  ticket.PhaseNeedsAttention,
	"closed":           ticket.PhaseClosed,
}

// closeLogWriter closes a blog writer and reports a failure to stderr without
// changing the command's exit status. A failed close means a log entry that
// the command already reported as written is not actually on disk; silently
// dropping that error is the one thing this codebase does not do.
func closeLogWriter(w *blog.Writer, stderr io.Writer) {
	if err := w.Close(); err != nil {
		fmt.Fprintf(stderr, "warning: closing log: %v\n", err)
	}
}

func TicketAdvance(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("ticket advance", flag.ContinueOnError)
	fs.SetOutput(stderr)
	repo := fs.String("repo", ".", "target repo root")
	id := fs.String("ticket", "", "ticket id (required)")
	to := fs.String("to", "", "phase to advance to (required)")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	phase, ok := knownPhases[*to]
	if !ok || *id == "" {
		fmt.Fprintln(stderr, "usage: golem ticket advance --ticket <id> --to <phase>")
		return 1
	}

	ticketDir := filepath.Join(*repo, ".golem", "tickets", *id)
	s, err := ticket.Load(ticketDir)
	if err != nil {
		fmt.Fprintf(stderr, "loading ticket %s: %v\n", *id, err)
		return 1
	}
	s.Phase = phase
	return boolToExit(s.Save(ticketDir))
}

func TicketReview(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("ticket review", flag.ContinueOnError)
	fs.SetOutput(stderr)
	repo := fs.String("repo", ".", "target repo root")
	id := fs.String("ticket", "", "ticket id (required)")
	base := fs.String("base", "", "base branch; the reviewer gets the diff from its merge-base")
	mf := addModelFlags(fs)
	if err := fs.Parse(args); err != nil {
		return 1
	}
	if *id == "" {
		fmt.Fprintln(stderr, "usage: golem ticket review --ticket <id>")
		return 1
	}

	golemDir := filepath.Join(*repo, ".golem")
	cfg, err := config.Load(filepath.Join(golemDir, "config.yaml"))
	if err != nil {
		fmt.Fprintf(stderr, "loading config: %v\n", err)
		return 1
	}
	if err := mf.validate(cfg); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	ticketDir := filepath.Join(golemDir, "tickets", *id)
	s, err := ticket.Load(ticketDir)
	if err != nil {
		fmt.Fprintf(stderr, "loading ticket %s: %v\n", *id, err)
		return 1
	}
	rolePrompt, err := os.ReadFile(filepath.Join(golemDir, "roles", "reviewer.md"))
	if err != nil {
		fmt.Fprintf(stderr, "reading reviewer role: %v\n", err)
		return 1
	}
	logPath := filepath.Join(ticketDir, "log.jsonl")
	// The gate runs first so the reviewer reads its evidence in the log.
	gateResult, err := runTicketGate(cfg, s, ticketDir, "gate", "gate evidence")
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}

	if gateResult.Failure != nil && gateResult.Failure.Kind == "environment" {
		s.Phase = ticket.PhaseNeedsAttention
		if err := s.Save(ticketDir); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		fmt.Fprint(stderr, gateResult.Output)
		return 1
	}
	entries, err := blog.ReadAll(logPath)
	if err != nil {
		fmt.Fprintf(stderr, "reading log: %v\n", err)
		return 1
	}
	entries = collapseLongEntries(entries, maxLogBodyBytes)

	runner, err := mf.runner(cfg, s.WorktreePath)
	if err != nil {
		fmt.Fprintf(stderr, "selecting backend: %v\n", err)
		return 1
	}
	model, err := mf.resolve(cfg, "reviewer", models.StageReview)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	// Review the whole branch: from the merge-base with the live base branch
	// (--base; the epic branch for an epic child), else the pinned base. The
	// pinned SHA goes stale once siblings merge, and a diff taken from it
	// then holds every sibling's work.
	from := reviewBase(s.WorktreePath, *base, s.BranchBaseSHA)
	diff, err := reviewDiff(s.WorktreePath, from, "HEAD", stderr)
	if err != nil {
		fmt.Fprintf(stderr, "getting branch diff: %v\n", err)
		return 1
	}
	// A diff over the cap cannot be reviewed whole, so no reviewer is run (its
	// verdict on a part would not be evidence): park with the fix instead.
	if limit := cfg.ReviewMaxDiffBytes(); len(diff) > limit {
		tooLarge := fmt.Sprintf("%s review diff too large (%d bytes, cap %d); no reviewer was run, so no approval was recorded. "+
			"Fix: merge or rebase onto the base branch if it carries unrelated work, split the ticket, or raise review.max_diff_bytes in .golem/config.yaml.",
			evidence.EnvironmentMarker, len(diff), limit)
		w, err := blog.NewWriter(logPath)
		if err != nil {
			fmt.Fprintf(stderr, "opening log: %v\n", err)
			return 1
		}
		err = w.Append(blog.NewEntry("reviewer", blog.TypeStatus, tooLarge)) // first line, so the shem's environment-failure detection finds it
		closeLogWriter(w, stderr)
		if err != nil {
			fmt.Fprintf(stderr, "recording attestation: %v\n", err)
			return 1
		}
		s.Phase = ticket.PhaseNeedsAttention
		if err := s.Save(ticketDir); err != nil {
			fmt.Fprintf(stderr, "saving ticket: %v\n", err)
			return 1
		}
		fmt.Fprintln(stderr, tooLarge)
		return 1
	}
	result, err := runner.RunAgent(context.Background(), "reviewer", agentrunner.Context{
		LogSlice:   entries,
		Diff:       diff,
		RolePrompt: string(rolePrompt) + reviewerVerdictContract, // the contract must not depend on an editable role file
	}, model)
	if err != nil {
		fmt.Fprintf(stderr, "running reviewer: %v\n", err)
		return 1
	}

	w, err := blog.NewWriter(logPath)
	if err != nil {
		fmt.Fprintf(stderr, "opening log: %v\n", err)
		return 1
	}
	attestation := blog.NewEntry("reviewer", blog.TypeStatus, result.Output)
	attestation.Model = result.Model
	if err := w.Append(attestation); err != nil {
		closeLogWriter(w, stderr)
		fmt.Fprintf(stderr, "recording attestation: %v\n", err)
		return 1
	}
	closeLogWriter(w, stderr)

	// A reviewer FAIL blocks the ticket like a failing gate. A missing
	// verdict blocks only tickets with acceptance commands (epic children,
	// which may auto-merge); other tickets keep the gate-only rule.
	pass, found := parseReviewerVerdict(result.Output)
	s.Phase = ticket.PhaseReadyForReview
	if !gateResult.Passed || (found && !pass) || (!found && len(s.AcceptanceCommands) > 0) {
		s.Phase = ticket.PhaseNeedsAttention
	}
	if err := s.Save(ticketDir); err != nil {
		fmt.Fprintf(stderr, "saving ticket: %v\n", err)
		return 1
	}

	fmt.Fprintln(stdout, result.Output)
	return 0
}

// maxLogBodyBytes bounds one STATUS or BLOCKER body in the reviewer's log
// slice. Nested gate output grew log files to 490 KB.
const maxLogBodyBytes = 4 << 10

// collapseLongEntries keeps the head and tail of an oversized STATUS or
// BLOCKER body, where a command's verdict and its last output usually are.
func collapseLongEntries(entries []blog.Entry, limit int) []blog.Entry {
	out := make([]blog.Entry, len(entries))
	copy(out, entries)
	for i, e := range out {
		if (e.Type != blog.TypeStatus && e.Type != blog.TypeBlocker) || len(e.Message) <= limit {
			continue
		}
		half := limit / 2
		out[i].Message = strings.ToValidUTF8(e.Message[:half], "") + fmt.Sprintf("\n[... %d bytes truncated ...]\n", len(e.Message)-2*half) +
			strings.ToValidUTF8(e.Message[len(e.Message)-half:], "")
	}
	return out
}

// reviewBase prefers the live base branch (--base) over the pinned SHA, but only
// when this checkout can resolve it: an unfetched base would make reviewDiff
// fall back to the last commit alone, and a partial review reads as a full one.
// A local:// repository has no origin, so origin/<b> falls back to <b>.
func reviewBase(worktree, flagBase, pinned string) string {
	local, _ := strings.CutPrefix(flagBase, "origin/")
	for _, b := range []string{flagBase, local} {
		if b != "" && revExists(worktree, b) {
			return b
		}
	}
	return pinned
}
