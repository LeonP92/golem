package cli

import (
	"flag"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/leonp92/golem/internal/graph"
)

func GraphCheckBoundary(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("graph check-boundary", flag.ContinueOnError)
	fs.SetOutput(stderr)
	repo := fs.String("repo", ".", "target repo root")
	subsystem := fs.String("subsystem", "", "subsystem name (required)")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	if *subsystem == "" {
		fmt.Fprintln(stderr, "usage: golem graph check-boundary --subsystem <name> [--repo <path>]")
		return 1
	}

	graphs, err := graph.LoadAllModuleGraphs(filepath.Join(*repo, ".golem", "index"))
	if err != nil {
		fmt.Fprintf(stderr, "loading graph: %v\n", err)
		return 1
	}
	if len(graphs) == 0 {
		fmt.Fprintln(stdout, "no graph index — run 'golem graph build'")
		return 0
	}

	inSubsystem := map[string]bool{}
	for _, g := range graphs {
		if strings.EqualFold(g.Subsystem, *subsystem) {
			inSubsystem[g.Module] = true
		}
	}
	if len(inSubsystem) == 0 {
		fmt.Fprintf(stdout, "no modules found in subsystem %s\n", *subsystem)
		return 0
	}

	violations := 0
	for _, g := range graphs {
		if inSubsystem[g.Module] {
			continue
		}
		for _, imp := range g.Imports {
			if inSubsystem[imp] {
				fmt.Fprintf(stdout, "VIOLATION  %s  →  %s\n", g.Module, imp)
				violations++
			}
		}
	}
	if violations == 0 {
		fmt.Fprintf(stdout, "no boundary violations for subsystem %s\n", *subsystem)
	} else {
		fmt.Fprintf(stdout, "\n%d violation(s)\n", violations)
	}
	return 0
}
