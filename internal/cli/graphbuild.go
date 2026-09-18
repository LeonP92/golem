package cli

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"

	"github.com/leonp92/golem/internal/config"
	"github.com/leonp92/golem/internal/graph"
)

func GraphBuild(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("graph build", flag.ContinueOnError)
	fs.SetOutput(stderr)
	repo := fs.String("repo", ".", "target repo root")
	concurrency := fs.Int("concurrency", 4, "max parallel file extractions")
	if err := fs.Parse(args); err != nil {
		return 1
	}

	golemDir := filepath.Join(*repo, ".golem")
	cfg, err := config.Load(filepath.Join(golemDir, "config.yaml"))
	if err != nil {
		fmt.Fprintf(stderr, "loading config: %v\n", err)
		return 1
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

	indexDir := filepath.Join(golemDir, "index")
	if err := os.MkdirAll(indexDir, 0o755); err != nil {
		fmt.Fprintf(stderr, "creating index dir: %v\n", err)
		return 1
	}

	graphs := buildModuleGraphs(*repo, modules, *concurrency, stderr)

	if err := writeGraphs(golemDir, graphs); err != nil {
		fmt.Fprintf(stderr, "writing graph: %v\n", err)
		return 1
	}

	// Subsystem-narrative pass (step 12) — hooked in after LLM cluster
	// invocations land; for now, WriteIndex called from writeGraphs uses
	// the deterministic layout with no narratives.

	commit, _ := gitHead(*repo)
	meta, _ := graph.LoadMeta(indexDir)
	if meta.Modules == nil {
		meta.Modules = map[string]graph.ModuleMeta{}
	}
	meta.BaseCommit = commit
	for i, m := range modules {
		hashes, _ := graph.ModuleHashes(*repo, m.Files)
		meta.Modules[graphs[i].Module] = graph.ModuleMeta{Commit: commit, FileHashes: hashes}
	}
	if err := graph.SaveMeta(indexDir, meta); err != nil {
		fmt.Fprintf(stderr, "saving graph meta: %v\n", err)
		return 1
	}
	if err := graph.SaveEdges(indexDir, graphs); err != nil {
		fmt.Fprintf(stderr, "saving graph edges: %v\n", err)
		return 1
	}

	fmt.Fprintf(stdout, "graph written to %s\n", filepath.Join(golemDir, "wiki", "graph"))
	return 0
}

// buildModuleGraphs runs tree-sitter extraction across every discovered
// module in parallel. No LLM is invoked here. Extraction failures are
// logged to stderr and the module gets an empty (but present) record so
// that downstream consumers still see it.
func buildModuleGraphs(repoRoot string, modules []graph.Module, concurrency int, stderr io.Writer) []graph.ModuleGraph {
	results := make([]graph.ModuleGraph, len(modules))
	sem := make(chan struct{}, concurrency)
	var wg sync.WaitGroup

	for i, m := range modules {
		wg.Add(1)
		go func(idx int, mod graph.Module) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			structural := aggregateModule(repoRoot, mod, stderr)

			g := graph.ModuleGraph{
				Module:        mod.Path,
				Subsystem:     graph.SubsystemForPath(mod.Path),
				PackageDoc:    structural.PackageDoc,
				Summary:       structural.PackageDoc,
				Imports:       structural.Imports,
				Consts:        structural.Consts,
				ExportedFuncs: structural.ExportedFuncs,
				ExportedTypes: structural.ExportedTypes,
				ExportFns:     structural.ExportFns,
				ExportTypes:   structural.ExportTypes,
			}
			results[idx] = g
		}(i, m)
	}
	wg.Wait()
	return results
}

// aggregateModule reads every file in a module, extracts structural data,
// and merges into a single StructuralData. First non-empty PackageDoc
// wins (there is only one package doc per Go/Python module in practice).
// Errors are logged, not returned — a corrupt file does not fail the
// whole graph build.
func aggregateModule(repoRoot string, mod graph.Module, stderr io.Writer) graph.StructuralData {
	agg := graph.StructuralData{}
	for _, relPath := range mod.Files {
		ext := filepath.Ext(relPath)
		langName := graph.LangForExt(ext)
		if langName == "" {
			continue
		}
		src, err := os.ReadFile(filepath.Join(repoRoot, relPath))
		if err != nil {
			fmt.Fprintf(stderr, "warning: reading %s: %v\n", relPath, err)
			continue
		}
		d, err := graph.Extract(src, langName)
		if err != nil {
			fmt.Fprintf(stderr, "warning: extracting %s: %v\n", relPath, err)
			continue
		}
		if agg.PackageDoc == "" && d.PackageDoc != "" {
			agg.PackageDoc = d.PackageDoc
		}
		agg.Imports = append(agg.Imports, d.Imports...)
		agg.Consts = append(agg.Consts, d.Consts...)
		agg.ExportFns = append(agg.ExportFns, d.ExportFns...)
		agg.ExportTypes = append(agg.ExportTypes, d.ExportTypes...)
		agg.ExportedFuncs = append(agg.ExportedFuncs, d.ExportedFuncs...)
		agg.ExportedTypes = append(agg.ExportedTypes, d.ExportedTypes...)
	}
	agg.Imports = dedupeStrings(agg.Imports)
	agg.Consts = dedupeStrings(agg.Consts)
	agg.ExportFns = dedupeStrings(agg.ExportFns)
	agg.ExportTypes = dedupeStrings(agg.ExportTypes)
	return agg
}

func dedupeStrings(ss []string) []string {
	if len(ss) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(ss))
	out := make([]string, 0, len(ss))
	for _, s := range ss {
		if _, ok := seen[s]; !ok {
			seen[s] = struct{}{}
			out = append(out, s)
		}
	}
	return out
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
