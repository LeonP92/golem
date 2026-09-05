package cli

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/leonp92/golem/internal/agentrunner"
	"github.com/leonp92/golem/internal/blog"
	"github.com/leonp92/golem/internal/config"
	"github.com/leonp92/golem/internal/gate"
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
	entries, err := blog.ReadAll(logPath)
	if err != nil {
		fmt.Fprintf(stderr, "reading log: %v\n", err)
		return 1
	}

	runner, err := NewRunner(cfg, s.WorktreePath)
	if err != nil {
		fmt.Fprintf(stderr, "selecting backend: %v\n", err)
		return 1
	}
	result, err := runner.RunAgent("reviewer", agentrunner.Context{
		LogSlice:   entries,
		RolePrompt: string(rolePrompt),
	})
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
		w.Close()
		fmt.Fprintf(stderr, "recording attestation: %v\n", err)
		return 1
	}
	w.Close()

	gateResult, err := gate.Run(s.WorktreePath, cfg.Gate)
	if err != nil {
		fmt.Fprintf(stderr, "running gate: %v\n", err)
		return 1
	}
	s.Phase = ticket.PhaseReadyForReview
	if !gateResult.Passed {
		s.Phase = ticket.PhaseNeedsAttention
	}
	if err := s.Save(ticketDir); err != nil {
		fmt.Fprintf(stderr, "saving ticket: %v\n", err)
		return 1
	}

	fmt.Fprintln(stdout, result.Output)
	return 0
}
