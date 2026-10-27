package cli

import (
	"flag"
	"fmt"
	"io"
	"path/filepath"

	"github.com/leonp92/golem/internal/blog"
	"github.com/leonp92/golem/internal/config"
	"github.com/leonp92/golem/internal/gate"
	"github.com/leonp92/golem/internal/ticket"
)

// TicketVerify runs the gate (plus the ticket's acceptance commands) as
// the worker's self-verification and logs the result. It never replaces
// the review gate. Exit codes: 0 passed, 1 could not run, 2 a check failed.
func TicketVerify(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("ticket verify", flag.ContinueOnError)
	fs.SetOutput(stderr)
	repo := fs.String("repo", ".", "target repo root")
	id := fs.String("ticket", "", "ticket id (required)")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	if *id == "" {
		fmt.Fprintln(stderr, "usage: golem ticket verify --ticket <id>")
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
	res, err := runTicketGate(cfg, s, ticketDir, "worker", "self-verification")
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	fmt.Fprint(stdout, res.Output)
	if !res.Passed {
		if res.Failure != nil && res.Failure.Kind == "environment" {
			return 1
		}
		return 2
	}
	return 0
}

// runTicketGate runs the repo's gate commands plus the ticket's acceptance
// commands and logs the result as evidence bound to the head it ran on.
func runTicketGate(cfg *config.Config, s *ticket.State, ticketDir, role, label string) (gate.Result, error) {
	g := cfg.Gate
	g.Commands = append(append([]string{}, cfg.Gate.Commands...), s.AcceptanceCommands...)
	res, err := gate.Run(s.WorktreePath, g)
	if err != nil {
		return res, fmt.Errorf("running gate: %w", err)
	}
	head, _ := gitHead(s.WorktreePath)
	w, err := blog.NewWriter(filepath.Join(ticketDir, "log.jsonl"))
	if err != nil {
		return res, fmt.Errorf("opening log: %w", err)
	}
	defer func() { _ = w.Close() }()
	entry := blog.NewEntry(role, blog.TypeStatus, fmt.Sprintf("%s: passed=%t head=%s environment_checks=%q commands=%q\n%s",
		label, res.Passed, head, g.EnvironmentChecks, g.Commands, res.Output))
	if err := w.Append(entry); err != nil {
		return res, fmt.Errorf("recording %s: %w", label, err)
	}
	return res, nil
}
