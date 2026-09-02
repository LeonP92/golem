package cli

import (
	"flag"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/leonpham/golem/internal/graph"
)

func GraphDeps(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("graph deps", flag.ContinueOnError)
	fs.SetOutput(stderr)
	repo := fs.String("repo", ".", "target repo root")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	if fs.NArg() == 0 {
		fmt.Fprintln(stderr, "usage: golem graph deps [--repo <path>] <module>")
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

	// Exact match first, then case-insensitive fallback.
	var found *graph.ModuleGraph
	for i := range graphs {
		if graphs[i].Module == target {
			found = &graphs[i]
			break
		}
	}
	if found == nil {
		for i := range graphs {
			if strings.EqualFold(graphs[i].Module, target) {
				found = &graphs[i]
				break
			}
		}
	}

	if found == nil {
		fmt.Fprintf(stdout, "module %s not found in graph index\n", target)
		return 0
	}
	if len(found.Imports) == 0 {
		fmt.Fprintf(stdout, "%s has no recorded imports\n", target)
		return 0
	}
	for _, imp := range found.Imports {
		fmt.Fprintln(stdout, imp)
	}
	return 0
}
