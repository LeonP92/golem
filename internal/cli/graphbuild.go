package cli

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
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

	graphs = graph.CoarsenSubsystems(graphs, graph.DefaultCoarsenClusterSize)

	// One LLM call per subsystem cluster; cached; parallel.
	narratives := runSubsystemNarratives(cfg, *repo, graphs, *concurrency, true, stdout, stderr)

	if err := writeGraphs(golemDir, graphs, narratives); err != nil {
		fmt.Fprintf(stderr, "writing graph: %v\n", err)
		return 1
	}

	commit, _ := gitHead(*repo)
	meta := &graph.Meta{BaseCommit: commit, Modules: make(map[string]graph.ModuleMeta, len(modules))}
	for _, m := range modules {
		hashes, _ := graph.ModuleHashes(*repo, m.Files)
		meta.Modules[m.Path] = graph.ModuleMeta{Commit: commit, FileHashes: hashes}
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

// runSubsystemNarratives produces one narrative per subsystem cluster,
// backed by a content-hash cache so only clusters whose membership or
// package docs changed reach the LLM. retryFailed re-asks for clusters
// whose last narrative was rejected (graph build); graph update keeps
// the cached rejection so an unrelated edit never re-bills it. Failures
// render as a visible stub and never block the build.
func runSubsystemNarratives(cfg *config.Config, repoRoot string, graphs []graph.ModuleGraph, concurrency int, retryFailed bool, stdout, stderr io.Writer) graph.SubsystemNarratives {
	golemDir := filepath.Join(repoRoot, ".golem")
	cache, err := graph.LoadNarrativeCache(filepath.Join(golemDir, "index", "subsystem-narratives.json"))
	if err != nil {
		fmt.Fprintf(stderr, "warning: loading narrative cache, starting empty: %v\n", err)
	}
	n := narrator{
		rolePrompt:  loadNarrativeRole(golemDir, stderr),
		cache:       cache,
		newRunner:   func() (agentrunner.Runner, error) { return NewRunner(cfg, repoRoot) },
		concurrency: concurrency,
		retryFailed: retryFailed,
		stdout:      stdout,
		stderr:      stderr,
	}
	out := n.narrate(graph.ClusterBySubsystem(graphs))
	if err := cache.Save(); err != nil {
		fmt.Fprintf(stderr, "warning: saving narrative cache: %v\n", err)
	}
	return out
}

// loadNarrativeRole returns the repo's graph-builder role, falling back to
// the embedded default when it is missing or still the pre-narrative
// per-module prompt (which asks for a conflicting output format).
func loadNarrativeRole(golemDir string, stderr io.Writer) string {
	data, err := os.ReadFile(filepath.Join(golemDir, "roles", "graph-builder.md"))
	if err == nil && !strings.Contains(string(data), "GRAPH_SUMMARY") {
		return string(data)
	}
	if err == nil {
		fmt.Fprintln(stderr, "warning: .golem/roles/graph-builder.md is the legacy per-module prompt; using the built-in narrative role")
	}
	def, _ := roles.Defaults.ReadFile("defaults/graph-builder.md")
	return string(def)
}

type narrator struct {
	rolePrompt  string
	cache       *graph.SubsystemNarrativeCache
	newRunner   func() (agentrunner.Runner, error)
	concurrency int
	retryFailed bool
	stdout      io.Writer
	stderr      io.Writer
}

type narrationTask struct {
	sub     string
	cluster []graph.ModuleGraph
	hash    string
}

func (n narrator) narrate(clusters map[string][]graph.ModuleGraph) graph.SubsystemNarratives {
	out := graph.SubsystemNarratives{}
	live := make(map[string]bool, len(clusters))
	var misses []narrationTask
	for sub, cluster := range clusters {
		hash := graph.ClusterHash(sub, cluster)
		live[hash] = true
		narrative, failed, ok := n.cache.Lookup(hash)
		switch {
		case ok && !failed:
			out[sub] = narrative
		case ok && !n.retryFailed:
			out[sub] = graph.StubNarrative(sub)
		default:
			misses = append(misses, narrationTask{sub: sub, cluster: cluster, hash: hash})
		}
	}
	n.cache.Prune(live)
	if len(misses) == 0 {
		return out
	}

	runner, err := n.newRunner()
	if err != nil {
		fmt.Fprintf(n.stderr, "warning: runner init failed, stub narratives: %v\n", err)
		for _, t := range misses {
			out[t.sub] = graph.StubNarrative(t.sub)
		}
		return out
	}

	var mu sync.Mutex
	sem := make(chan struct{}, max(n.concurrency, 1))
	var wg sync.WaitGroup
	for _, t := range misses {
		wg.Add(1)
		go func(t narrationTask) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			narrative := n.narrateOne(runner, t)
			mu.Lock()
			out[t.sub] = narrative
			mu.Unlock()
		}(t)
	}
	wg.Wait()
	return out
}

// narrateOne asks the LLM for one cluster. Runner errors are transient
// and left uncached; quality-gate rejections are cached as failures.
func (n narrator) narrateOne(runner agentrunner.Runner, t narrationTask) string {
	fmt.Fprintf(n.stdout, "narrating subsystem %q (%d modules)...\n", t.sub, len(t.cluster))
	ctx := agentrunner.Context{RolePrompt: n.rolePrompt + "\n\n" + graph.BuildClusterPrompt(t.sub, t.cluster)}
	res, err := runner.RunAgent("graph-builder", ctx)
	if err != nil {
		fmt.Fprintf(n.stderr, "warning: narrative call for %q failed: %v\n", t.sub, err)
		return graph.StubNarrative(t.sub)
	}
	narrative := graph.ExtractNarrativeJSON(res.Output)
	if err := graph.ValidateNarrative(narrative, t.cluster); err != nil {
		fmt.Fprintf(n.stderr, "warning: narrative for %q rejected: %v\n", t.sub, err)
		n.cache.SetFailed(t.sub, t.hash)
		return graph.StubNarrative(t.sub)
	}
	n.cache.Set(t.sub, t.hash, narrative)
	return narrative
}

func writeGraphs(golemDir string, graphs []graph.ModuleGraph, narratives graph.SubsystemNarratives) error {
	wikiDir := filepath.Join(golemDir, "wiki")
	indexDir := filepath.Join(golemDir, "index")
	if err := graph.ResetAggregates(wikiDir); err != nil {
		return err
	}
	if err := graph.ResetModuleGraphs(indexDir, wikiDir); err != nil {
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
	return graph.WriteIndex(wikiDir, graphs, narratives)
}
