package cli

import (
	"flag"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/leonp92/golem/internal/wiki"
)

func wikiPaths(repo string) (wikiDir, indexPath string) {
	golemDir := filepath.Join(repo, ".golem")
	return filepath.Join(golemDir, "wiki"), filepath.Join(golemDir, "index", "vectors.db")
}

func WikiSearch(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("wiki search", flag.ContinueOnError)
	fs.SetOutput(stderr)
	repo := fs.String("repo", ".", "target repo root")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	query := strings.Join(fs.Args(), " ")
	if query == "" {
		fmt.Fprintln(stderr, "usage: golem wiki search <query>")
		return 1
	}

	wikiDir, indexPath := wikiPaths(*repo)
	idx, err := wiki.EnsureIndex(wikiDir, indexPath)
	if err != nil {
		fmt.Fprintf(stderr, "loading wiki index: %v\n", err)
		return 1
	}

	for _, m := range idx.SearchExpanded(query, 5) {
		if m.Via != "" {
			fmt.Fprintf(stdout, "%.3f  %s  (via %s)\n", m.Score, m.Path, m.Via)
		} else {
			fmt.Fprintf(stdout, "%.3f  %s\n", m.Score, m.Path)
		}
	}
	return 0
}

func WikiRebuild(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("wiki rebuild", flag.ContinueOnError)
	fs.SetOutput(stderr)
	repo := fs.String("repo", ".", "target repo root")
	if err := fs.Parse(args); err != nil {
		return 1
	}

	wikiDir, indexPath := wikiPaths(*repo)
	docs, err := wiki.LoadAll(wikiDir)
	if err != nil {
		fmt.Fprintf(stderr, "loading wiki: %v\n", err)
		return 1
	}
	idx := wiki.Build(docs)
	if err := wiki.SaveToFile(idx, indexPath); err != nil {
		fmt.Fprintf(stderr, "saving index: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "rebuilt index from %d wiki documents\n", len(docs))
	return 0
}
