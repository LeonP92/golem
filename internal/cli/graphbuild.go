package cli

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"

	"github.com/leonp92/golem/internal/agentrunner"
	"github.com/leonp92/golem/internal/config"
	"github.com/leonp92/golem/internal/graph"
	"github.com/leonp92/golem/internal/roles"
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

	// Subsystem-narrative pass — one LLM call per subsystem cluster, cached,
	// parallelised at the same concurrency as file extraction.
	narratives := runSubsystemNarratives(cfg, *repo, graphs, *concurrency, stdout, stderr)

	if err := writeGraphs(golemDir, graphs, narratives); err != nil {
		fmt.Fprintf(stderr, "writing graph: %v\n", err)
		return 1
	}

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

// runSubsystemNarratives clusters modules by subsystem and calls the
// graph-builder role once per cluster. Uses a persistent content-hash
// cache — unchanged clusters skip the LLM entirely. Failure or
// validation reject falls back to a visible stub, never blocks the
// build. Cache-miss clusters are dispatched concurrently up to the
// concurrency cap; on a large monorepo (~60 subsystems) this collapses
// the narrative phase from ~7 min sequential to ~1-2 min.
func runSubsystemNarratives(cfg *config.Config, repoRoot string, graphs []graph.ModuleGraph, concurrency int, stdout, stderr io.Writer) graph.SubsystemNarratives {
	out := graph.SubsystemNarratives{}
	clusters := graph.ClusterBySubsystem(graphs)
	if len(clusters) == 0 {
		return out
	}
	if concurrency < 1 {
		concurrency = 1
	}
	golemDir := filepath.Join(repoRoot, ".golem")
	cachePath := filepath.Join(golemDir, "index", "subsystem-narratives.json")
	cache, cerr := graph.LoadNarrativeCache(cachePath)
	if cerr != nil {
		fmt.Fprintf(stderr, "warning: loading narrative cache: %v\n", cerr)
		cache, _ = graph.LoadNarrativeCache(filepath.Join(os.TempDir(), "sn.json"))
	}

	rolePrompt, err := os.ReadFile(filepath.Join(golemDir, "roles", "graph-builder.md"))
	if err != nil {
		data, rerr := roles.Defaults.ReadFile("defaults/graph-builder.md")
		if rerr != nil {
			fmt.Fprintf(stderr, "warning: graph-builder role missing; using stub narratives: %v\n", err)
			for sub := range clusters {
				out[sub] = graph.StubNarrative(sub)
			}
			return out
		}
		rolePrompt = data
	}

	subs := make([]string, 0, len(clusters))
	for s := range clusters {
		subs = append(subs, s)
	}

	// First pass: consume cache hits synchronously; collect misses to dispatch.
	type task struct {
		sub     string
		cluster []graph.ModuleGraph
		hash    string
	}
	var misses []task
	for _, sub := range subs {
		cluster := clusters[sub]
		hash := graph.ClusterHash(sub, cluster)
		if cached, ok := cache.Get(hash); ok {
			out[sub] = cached
			continue
		}
		misses = append(misses, task{sub: sub, cluster: cluster, hash: hash})
	}
	if len(misses) == 0 {
		return out
	}

	// Runner init is a one-time cost — do it before spawning workers so
	// a construction failure fails-safe to stubs without racing.
	runner, err := NewRunner(cfg, repoRoot)
	if err != nil {
		fmt.Fprintf(stderr, "warning: runner init failed, stub narratives: %v\n", err)
		for _, m := range misses {
			out[m.sub] = graph.StubNarrative(m.sub)
		}
		return out
	}

	var outMu sync.Mutex
	sem := make(chan struct{}, concurrency)
	var wg sync.WaitGroup
	for _, m := range misses {
		wg.Add(1)
		go func(m task) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			fmt.Fprintf(stdout, "narrating subsystem %q (%d modules)...\n", m.sub, len(m.cluster))
			body := graph.BuildClusterPrompt(m.sub, m.cluster)
			ctx := agentrunner.Context{RolePrompt: string(rolePrompt) + "\n\n---\n\nReturn a single JSON object: {\"subsystem\":\"" + m.sub + "\",\"narrative\":\"<>\"}. The narrative should be 2-4 sentences of cross-cutting context that references specific module names in this cluster.\n\n" + body}
			res, runErr := runner.RunAgent("graph-builder", ctx)
			if runErr != nil {
				fmt.Fprintf(stderr, "warning: narrative call for %q failed: %v\n", m.sub, runErr)
				outMu.Lock()
				out[m.sub] = graph.StubNarrative(m.sub)
				outMu.Unlock()
				return
			}
			narrative := graph.ExtractNarrativeJSON(res.Output)
			if vErr := graph.ValidateNarrative(narrative, m.cluster); vErr != nil {
				fmt.Fprintf(stderr, "warning: narrative for %q rejected: %v\n", m.sub, vErr)
				outMu.Lock()
				out[m.sub] = graph.StubNarrative(m.sub)
				outMu.Unlock()
				return
			}
			outMu.Lock()
			out[m.sub] = narrative
			outMu.Unlock()
			cache.Set(m.sub, m.hash, narrative)
		}(m)
	}
	wg.Wait()

	if err := cache.Save(); err != nil {
		fmt.Fprintf(stderr, "warning: saving narrative cache: %v\n", err)
	}
	return out
}

func writeGraphs(golemDir string, graphs []graph.ModuleGraph, narratives graph.SubsystemNarratives) error {
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
	if len(narratives) > 0 {
		return graph.WriteIndex(wikiDir, graphs, narratives)
	}
	return graph.WriteIndex(wikiDir, graphs)
}
