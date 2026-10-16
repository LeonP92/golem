package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/leonp92/golem/internal/agentrunner"
	"github.com/leonp92/golem/internal/blog"
	"github.com/leonp92/golem/internal/config"
	"github.com/leonp92/golem/internal/gate"
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
	// The verdict contract travels with the invocation, not only with the
	// role file. Role files are unpacked once and never overwritten (the
	// repo owns them), so an install initialised before the contract existed
	// — or a repo that customized the file and dropped it — would otherwise
	// produce attestations with no verdict and park every review in
	// needs-attention. The parsing contract must not depend on an editable
	// file for the gate to keep working.
	rolePromptWithContract := string(rolePrompt) + reviewerVerdictContract
	logPath := filepath.Join(ticketDir, "log.jsonl")

	// The gate runs first and its evidence, bound to the exact head, is
	// logged before the reviewer reads the log: the reviewer may only pass
	// a gate it saw run.
	gateCfg := ticketGate(cfg, s)
	gateResult, err := gate.Run(s.WorktreePath, gateCfg)
	if err != nil {
		fmt.Fprintf(stderr, "running gate: %v\n", err)
		return 1
	}
	w, err := blog.NewWriter(logPath)
	if err != nil {
		fmt.Fprintf(stderr, "opening log: %v\n", err)
		return 1
	}
	evidence := blog.NewEntry("gate", blog.TypeStatus, fmt.Sprintf(
		"gate evidence: head=%s commands_run=%d passed=%t commands=%q\n%s", gateResult.HeadSHA, gateResult.CommandsRun,
		gateResult.Passed, gateCfg.Commands, gateResult.Output))
	if err := w.Append(evidence); err != nil {
		closeLogWriter(w, stderr)
		fmt.Fprintf(stderr, "recording gate evidence: %v\n", err)
		return 1
	}
	closeLogWriter(w, stderr)

	entries, err := blog.ReadAll(logPath)
	if err != nil {
		fmt.Fprintf(stderr, "reading log: %v\n", err)
		return 1
	}

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
	// The verdict gates the ticket: diff since the pinned base, else --base.
	from := s.BranchBaseSHA
	if from == "" {
		from = *base
	}
	diff, err := reviewDiff(s.WorktreePath, from, "HEAD", stderr)
	if err != nil {
		fmt.Fprintf(stderr, "getting branch diff: %v\n", err)
		return 1
	}
	result, err := runner.RunAgent(context.Background(), "reviewer", agentrunner.Context{
		LogSlice:   entries,
		Diff:       diff,
		RolePrompt: rolePromptWithContract,
	}, model)
	if err != nil {
		fmt.Fprintf(stderr, "running reviewer: %v\n", err)
		return 1
	}

	w, err = blog.NewWriter(logPath)
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

	// Ready-for-review requires BOTH a structured reviewer PASS and a
	// passing gate. The reviewer's text used to be recorded and ignored;
	// the phase came from the gate alone, so a reviewer that found a
	// blocker could not stop the ticket. A missing or unreadable verdict
	// fails closed: evidence that cannot be read is never success.
	verdictPass, verdictFound := parseReviewerVerdict(result.Output)
	s.Phase = ticket.PhaseReadyForReview
	if !verdictFound || !verdictPass || !gateResult.Passed {
		s.Phase = ticket.PhaseNeedsAttention
	}
	if err := s.Save(ticketDir); err != nil {
		fmt.Fprintf(stderr, "saving ticket: %v\n", err)
		return 1
	}

	fmt.Fprintln(stdout, result.Output)
	return 0
}
