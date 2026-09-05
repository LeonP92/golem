package cli

import (
	"flag"
	"fmt"
	"io"
	"path/filepath"

	"github.com/leonp92/golem/internal/blog"
	"github.com/leonp92/golem/internal/ticket"
)

func TicketResume(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("ticket resume", flag.ContinueOnError)
	fs.SetOutput(stderr)
	repo := fs.String("repo", ".", "target repo root")
	id := fs.String("id", "", "ticket id (required)")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	if *id == "" {
		fmt.Fprintln(stderr, "usage: golem ticket resume --id <id>")
		return 1
	}

	ticketDir := filepath.Join(*repo, ".golem", "tickets", *id)
	s, err := ticket.Load(ticketDir)
	if err != nil {
		fmt.Fprintf(stderr, "loading ticket %s: %v\n", *id, err)
		return 1
	}

	entries, err := blog.ReadAll(filepath.Join(ticketDir, "log.jsonl"))
	if err != nil {
		fmt.Fprintf(stderr, "reading ticket log: %v\n", err)
		return 1
	}

	fmt.Fprintf(stdout, "ticket %s, phase: %s, branch: %s\n", s.ID, s.Phase, s.Branch)
	if len(entries) > 0 {
		last := entries[len(entries)-1]
		fmt.Fprintf(stdout, "last log entry [%s] %s: %s\n", last.Type, last.Role, last.Message)
	} else {
		fmt.Fprintln(stdout, "no log entries yet")
	}
	return 0
}
