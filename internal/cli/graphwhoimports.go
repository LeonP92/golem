package cli

import (
	"flag"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/leonpham/golem/internal/graph"
)

func GraphWhoImports(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("graph who-imports", flag.ContinueOnError)
	fs.SetOutput(stderr)
	repo := fs.String("repo", ".", "target repo root")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	if fs.NArg() == 0 {
		fmt.Fprintln(stderr, "usage: golem graph who-imports [--repo <path>] <module>")
		return 1
	}
	target := fs.Arg(0)

	graphs, err := graph.LoadAllModuleGraphs(filepath.Join(*repo, ".golem", "index"))
	if err != nil {
		fmt.Fprintf(stderr, "loading graph: %v\n", err)
		return 1
	}
	if len(graphs) == 0 {
		fmt.Fprintln(stdout, "no graph index — run 'golem graph build'")
		return 0
	}

	found := 0
	for _, g := range graphs {
		for _, imp := range g.Imports {
			if strings.EqualFold(imp, target) {
				fmt.Fprintln(stdout, g.Module)
				found++
				break
			}
		}
	}
	if found == 0 {
		fmt.Fprintf(stdout, "no modules import %s\n", target)
	}
	return 0
}
