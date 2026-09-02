package cli

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/leonpham/golem/internal/ticket"
)

func Tickets(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("tickets", flag.ContinueOnError)
	fs.SetOutput(stderr)
	repo := fs.String("repo", ".", "target repo root")
	if err := fs.Parse(args); err != nil {
		return 1
	}

	ticketsDir := filepath.Join(*repo, ".golem", "tickets")
	entries, err := os.ReadDir(ticketsDir)
	if os.IsNotExist(err) {
		fmt.Fprintln(stdout, "no tickets")
		return 0
	}
	if err != nil {
		fmt.Fprintf(stderr, "reading tickets dir: %v\n", err)
		return 1
	}

	fmt.Fprintf(stdout, "%-12s %-20s %-20s\n", "ID", "PHASE", "BRANCH")
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		s, err := ticket.Load(filepath.Join(ticketsDir, entry.Name()))
		if err != nil {
			continue
		}
		fmt.Fprintf(stdout, "%-12s %-20s %-20s\n", s.ID, s.Phase, s.Branch)
	}
	return 0
}
