package cli

import (
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/leonp92/golem/internal/config"
	"github.com/leonp92/golem/internal/graph"
	"github.com/leonp92/golem/internal/roles"
)

func GraphUpdate(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("graph update", flag.ContinueOnError)
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

	meta, err := graph.LoadMeta(filepath.Join(golemDir, "index"))
	if err != nil || meta.BaseCommit == "" {
		fmt.Fprintln(stderr, "no graph index found; run 'golem graph build' first")
		return 1
	}

	changed, err := changedFiles(*repo, meta.BaseCommit)
	if err != nil {
		fmt.Fprintf(stderr, "git diff: %v\n", err)
		return 1
	}
	if len(changed) == 0 {
		fmt.Fprintln(stdout, "graph is up to date")
		return 0
	}

	rolePrompt, err := os.ReadFile(filepath.Join(golemDir, "roles", "graph-builder.md"))
	if err != nil {
		data, _ := roles.Defaults.ReadFile("defaults/graph-builder.md")
		rolePrompt = data
	}

	stale := staleModules(changed, meta)
	modules, err := graph.Discover(*repo, cfg.Graph.MaxFileSizeKB, cfg.Graph.ExtraExtensions, cfg.Graph.IgnorePatterns)
	if err != nil {
		fmt.Fprintf(stderr, "discovering modules: %v\n", err)
		return 1
	}

	var toRebuild []graph.Module
	for _, m := range modules {
		if stale[m.Path] {
			toRebuild = append(toRebuild, m)
		}
	}

	wikiDir := filepath.Join(golemDir, "wiki")
	fmt.Fprintf(stdout, "updating %d stale modules...\n", len(toRebuild))
	graphs, err := runModules(runner, string(rolePrompt), *repo, wikiDir, toRebuild, *concurrency)
	if err != nil {
		fmt.Fprintf(stderr, "graph update: %v\n", err)
		return 1
	}

	indexDir := filepath.Join(golemDir, "index")

	for _, g := range graphs {
		if err := graph.WriteModule(wikiDir, g); err != nil {
			fmt.Fprintf(stderr, "writing module: %v\n", err)
			return 1
		}
		if err := graph.SaveModuleGraph(indexDir, g); err != nil {
			fmt.Fprintf(stderr, "saving module graph: %v\n", err)
			return 1
		}
	}

	allGraphs, err := graph.LoadAllModuleGraphs(indexDir)
	if err != nil {
		fmt.Fprintf(stderr, "loading module graphs: %v\n", err)
		return 1
	}
	if err := graph.ResetAggregates(wikiDir); err != nil {
		fmt.Fprintf(stderr, "resetting aggregates: %v\n", err)
		return 1
	}
	for _, g := range allGraphs {
		graph.AppendSymbols(wikiDir, g)
		graph.AppendTypes(wikiDir, g)
	}
	if err := graph.WriteIndex(wikiDir, allGraphs); err != nil {
		fmt.Fprintf(stderr, "writing index: %v\n", err)
		return 1
	}

	commit, _ := gitHead(*repo)
	meta.BaseCommit = commit
	for i, m := range toRebuild {
		hashes, _ := graph.ModuleHashes(*repo, m.Files)
		meta.Modules[graphs[i].Module] = graph.ModuleMeta{Commit: commit, FileHashes: hashes}
	}
	if err := graph.SaveMeta(indexDir, meta); err != nil {
		fmt.Fprintf(stderr, "saving graph meta: %v\n", err)
		return 1
	}
	if err := graph.SaveEdges(indexDir, allGraphs); err != nil {
		fmt.Fprintf(stderr, "saving graph edges: %v\n", err)
		return 1
	}

	fmt.Fprintln(stdout, "graph updated")
	return 0
}

func changedFiles(repoRoot, since string) ([]string, error) {
	cmd := exec.Command("git", "diff", "--name-only", since+"..HEAD")
	cmd.Dir = repoRoot
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	var files []string
	for _, f := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if f != "" {
			files = append(files, f)
		}
	}
	return files, nil
}

func staleModules(changed []string, meta *graph.Meta) map[string]bool {
	stale := map[string]bool{}
	for _, f := range changed {
		dir := filepath.ToSlash(filepath.Dir(f))
		stale[dir] = true
	}
	return stale
}

