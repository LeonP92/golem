package cli

import (
	"flag"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/leonp92/golem/internal/config"
	"github.com/leonp92/golem/internal/graph"
)

func GraphUpdate(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("graph update", flag.ContinueOnError)
	fs.SetOutput(stderr)
	repo := fs.String("repo", ".", "target repo root")
	concurrency := fs.Int("concurrency", 4, "max parallel module extractions and narrative calls")
	if err := fs.Parse(args); err != nil {
		return 1
	}

	golemDir := filepath.Join(*repo, ".golem")
	cfg, err := config.Load(filepath.Join(golemDir, "config.yaml"))
	if err != nil {
		fmt.Fprintf(stderr, "loading config: %v\n", err)
		return 1
	}

	indexDir := filepath.Join(golemDir, "index")
	wikiDir := filepath.Join(golemDir, "wiki")

	meta, err := graph.LoadMeta(indexDir)
	if err != nil || meta.BaseCommit == "" {
		fmt.Fprintln(stderr, "no graph index found; run 'golem graph build' first")
		return 1
	}
	if meta.Modules == nil {
		meta.Modules = map[string]graph.ModuleMeta{}
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

	stale := staleModules(changed)
	modules, err := graph.Discover(*repo, cfg.Graph.MaxFileSizeKB, cfg.Graph.ExtraExtensions, cfg.Graph.IgnorePatterns)
	if err != nil {
		fmt.Fprintf(stderr, "discovering modules: %v\n", err)
		return 1
	}

	var toRebuild []graph.Module
	for _, m := range modules {
		if stale[m.Path] {
			toRebuild = append(toRebuild, m)
			delete(stale, m.Path)
		}
	}
	// Whatever is left no longer holds any source files: drop it.
	for dir := range stale {
		if err := graph.DeleteModuleGraph(indexDir, wikiDir, dir); err != nil {
			fmt.Fprintf(stderr, "deleting module graph %s: %v\n", dir, err)
		}
		delete(meta.Modules, dir)
	}

	fmt.Fprintf(stdout, "updating %d stale modules...\n", len(toRebuild))
	rebuilt := buildModuleGraphs(*repo, toRebuild, *concurrency, stderr)

	stored, err := graph.LoadAllModuleGraphs(indexDir)
	if err != nil {
		fmt.Fprintf(stderr, "loading module graphs: %v\n", err)
		return 1
	}
	allGraphs, dirty := mergeModuleGraphs(stored, rebuilt)

	// Coarsen against the full graph so tags match a fresh build. A module
	// whose tag moved needs its page re-rendered even if its source didn't.
	prevTags := make(map[string]string, len(allGraphs))
	for _, g := range allGraphs {
		prevTags[g.Module] = g.Subsystem
	}
	allGraphs = graph.CoarsenSubsystems(allGraphs, graph.DefaultCoarsenClusterSize)
	for _, g := range allGraphs {
		if !dirty[g.Module] && prevTags[g.Module] == g.Subsystem {
			continue
		}
		if err := graph.WriteModule(wikiDir, g); err != nil {
			fmt.Fprintf(stderr, "writing module: %v\n", err)
			return 1
		}
		if err := graph.SaveModuleGraph(indexDir, g); err != nil {
			fmt.Fprintf(stderr, "saving module graph: %v\n", err)
			return 1
		}
	}

	if err := graph.ResetAggregates(wikiDir); err != nil {
		fmt.Fprintf(stderr, "resetting aggregates: %v\n", err)
		return 1
	}
	for _, g := range allGraphs {
		if err := graph.AppendSymbols(wikiDir, g); err != nil {
			fmt.Fprintf(stderr, "writing symbols: %v\n", err)
			return 1
		}
		if err := graph.AppendTypes(wikiDir, g); err != nil {
			fmt.Fprintf(stderr, "writing types: %v\n", err)
			return 1
		}
	}
	narratives := runSubsystemNarratives(cfg, *repo, allGraphs, *concurrency, false, stdout, stderr)
	if err := graph.WriteIndex(wikiDir, allGraphs, narratives); err != nil {
		fmt.Fprintf(stderr, "writing index: %v\n", err)
		return 1
	}

	commit, _ := gitHead(*repo)
	meta.BaseCommit = commit
	for _, m := range toRebuild {
		hashes, _ := graph.ModuleHashes(*repo, m.Files)
		meta.Modules[m.Path] = graph.ModuleMeta{Commit: commit, FileHashes: hashes}
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

// mergeModuleGraphs overlays freshly rebuilt graphs onto the stored set,
// returning the merged set and the modules that were rebuilt.
func mergeModuleGraphs(stored, rebuilt []graph.ModuleGraph) ([]graph.ModuleGraph, map[string]bool) {
	dirty := make(map[string]bool, len(rebuilt))
	for _, g := range rebuilt {
		dirty[g.Module] = true
	}
	merged := make([]graph.ModuleGraph, 0, len(stored)+len(rebuilt))
	for _, g := range stored {
		if !dirty[g.Module] {
			merged = append(merged, g)
		}
	}
	return append(merged, rebuilt...), dirty
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

// staleModules maps changed files (including deleted ones) to their
// module directory, matching how Discover groups files.
func staleModules(changed []string) map[string]bool {
	stale := map[string]bool{}
	for _, f := range changed {
		stale[filepath.ToSlash(filepath.Dir(f))] = true
	}
	return stale
}

