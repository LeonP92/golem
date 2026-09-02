package cli

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/leonpham/golem/internal/agentrunner"
	"github.com/leonpham/golem/internal/config"
	"github.com/leonpham/golem/internal/graph"
	"github.com/leonpham/golem/internal/roles"
)

func GraphBuild(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("graph build", flag.ContinueOnError)
	fs.SetOutput(stderr)
	repo := fs.String("repo", ".", "target repo root")
	concurrency := fs.Int("concurrency", 4, "max parallel agent invocations")
	if err := fs.Parse(args); err != nil {
		return 1
	}

	golemDir := filepath.Join(*repo, ".golem")
	cfg, err := config.Load(filepath.Join(golemDir, "config.yaml"))
	if err != nil {
		fmt.Fprintf(stderr, "loading config: %v\n", err)
		return 1
	}
	runner, err := NewRunner(cfg, *repo)
	if err != nil {
		fmt.Fprintf(stderr, "initialising runner: %v\n", err)
		return 1
	}

	rolePrompt, err := os.ReadFile(filepath.Join(golemDir, "roles", "graph-builder.md"))
	if err != nil {
		// fall back to embedded default
		data, rerr := roles.Defaults.ReadFile("defaults/graph-builder.md")
		if rerr != nil {
			fmt.Fprintf(stderr, "loading graph-builder role: %v\n", err)
			return 1
		}
		rolePrompt = data
	}

	modules, err := graph.Discover(*repo, cfg.Graph.MaxFileSizeKB, cfg.Graph.ExtraExtensions, cfg.Graph.IgnorePatterns)
	if err != nil {
		fmt.Fprintf(stderr, "discovering modules: %v\n", err)
		return 1
	}
	if len(modules) == 0 {
		fmt.Fprintln(stdout, "no source files found")
		return 0
	}

	fmt.Fprintf(stdout, "building graph for %d modules (concurrency %d)...\n", len(modules), *concurrency)

	wikiDir := filepath.Join(golemDir, "wiki")
	graphs, err := runModules(runner, string(rolePrompt), *repo, wikiDir, modules, *concurrency)
	if err != nil {
		fmt.Fprintf(stderr, "graph build: %v\n", err)
		return 1
	}

	if err := writeGraphs(golemDir, graphs); err != nil {
		fmt.Fprintf(stderr, "writing graph: %v\n", err)
		return 1
	}

	commit, _ := gitHead(*repo)
	meta, _ := graph.LoadMeta(filepath.Join(golemDir, "index"))
	if meta.Modules == nil {
		meta.Modules = map[string]graph.ModuleMeta{}
	}
	meta.BaseCommit = commit
	for i, m := range modules {
		hashes, _ := graph.ModuleHashes(*repo, m.Files)
		meta.Modules[graphs[i].Module] = graph.ModuleMeta{Commit: commit, FileHashes: hashes}
	}
	if err := graph.SaveMeta(filepath.Join(golemDir, "index"), meta); err != nil {
		fmt.Fprintf(stderr, "saving graph meta: %v\n", err)
		return 1
	}
	if err := graph.SaveEdges(filepath.Join(golemDir, "index"), graphs); err != nil {
		fmt.Fprintf(stderr, "saving graph edges: %v\n", err)
		return 1
	}

	fmt.Fprintf(stdout, "graph written to %s\n", filepath.Join(golemDir, "wiki", "graph"))
	return 0
}

func runModules(runner agentrunner.Runner, rolePrompt, repoRoot, wikiDir string, modules []graph.Module, concurrency int) ([]graph.ModuleGraph, error) {
	results := make([]graph.ModuleGraph, len(modules))
	errs := make([]error, len(modules))

	sem := make(chan struct{}, concurrency)
	var wg sync.WaitGroup

	for i, m := range modules {
		wg.Add(1)
		go func(idx int, mod graph.Module) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			ctx := agentrunner.Context{
				RolePrompt: rolePrompt + "\n\n" + buildModulePrompt(repoRoot, wikiDir, mod),
			}
			res, err := runner.RunAgent("graph-builder", ctx)
			if err != nil {
				errs[idx] = err
				return
			}
			results[idx] = graph.Parse(res.Output)
			if results[idx].Module == "" {
				results[idx].Module = mod.Path
			}
		}(i, m)
	}
	wg.Wait()

	for _, err := range errs {
		if err != nil {
			return nil, err
		}
	}
	return results, nil
}

func writeGraphs(golemDir string, graphs []graph.ModuleGraph) error {
	wikiDir := filepath.Join(golemDir, "wiki")
	indexDir := filepath.Join(golemDir, "index")
	if err := graph.ResetAggregates(wikiDir); err != nil {
		return err
	}
	for _, g := range graphs {
		if err := graph.WriteModule(wikiDir, g); err != nil {
			return err
		}
		if err := graph.AppendSymbols(wikiDir, g); err != nil {
			return err
		}
		if err := graph.AppendTypes(wikiDir, g); err != nil {
			return err
		}
		if err := graph.SaveModuleGraph(indexDir, g); err != nil {
			return err
		}
	}
	return graph.WriteIndex(wikiDir, graphs)
}

func buildModulePrompt(repoRoot, wikiDir string, m graph.Module) string {
	s := fmt.Sprintf("Module path: %s\nFiles:\n", m.Path)
	for _, f := range m.Files {
		data, err := os.ReadFile(filepath.Join(repoRoot, f))
		if err != nil {
			continue
		}
		lines := len(strings.Split(string(data), "\n"))
		s += fmt.Sprintf("\n--- %s (%d lines) ---\n%s\n", f, lines, data)
	}
	if index, err := os.ReadFile(filepath.Join(wikiDir, "graph", "index.md")); err == nil {
		s += "\n\n--- existing codebase index (for cross-module context) ---\n" + string(index)
	}
	return s
}
