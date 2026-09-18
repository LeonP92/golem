package cli

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/leonp92/golem/internal/agentrunner"
	"github.com/leonp92/golem/internal/graph"
)

type fakeRunner struct {
	output string
	err    error
	calls  atomic.Int32
}

func (f *fakeRunner) RunAgent(string, agentrunner.Context) (agentrunner.Result, error) {
	f.calls.Add(1)
	return agentrunner.Result{Output: f.output}, f.err
}

func (f *fakeRunner) WorktreeSetup(string) error { return nil }

const goodNarrative = `{"narrative":"The api layer exposes handlers while the store module persists records, so api depends on store for every write path."}`

func testNarrator(t *testing.T, r *fakeRunner, retryFailed bool) (narrator, *graph.SubsystemNarrativeCache) {
	t.Helper()
	cache, err := graph.LoadNarrativeCache(filepath.Join(t.TempDir(), "n.json"))
	if err != nil {
		t.Fatal(err)
	}
	return narrator{
		cache:       cache,
		newRunner:   func() (agentrunner.Runner, error) { return r, nil },
		concurrency: 2,
		retryFailed: retryFailed,
		stdout:      io.Discard,
		stderr:      io.Discard,
	}, cache
}

var testClusters = map[string][]graph.ModuleGraph{
	"orchestrator": {{Module: "internal/orchestrator/api"}, {Module: "internal/orchestrator/store"}},
}

func TestNarrate_cacheHitSkipsLLM(t *testing.T) {
	r := &fakeRunner{output: goodNarrative}
	n, _ := testNarrator(t, r, false)
	first := n.narrate(testClusters)
	second := n.narrate(testClusters)
	if got := r.calls.Load(); got != 1 {
		t.Fatalf("RunAgent calls = %d, want 1", got)
	}
	if first["orchestrator"] != second["orchestrator"] || strings.Contains(first["orchestrator"], "unavailable") {
		t.Errorf("unexpected narratives: %q / %q", first["orchestrator"], second["orchestrator"])
	}
}

func TestNarrate_rejectedCachedUnlessRetry(t *testing.T) {
	r := &fakeRunner{output: `{"narrative":"too short"}`}
	n, cache := testNarrator(t, r, false)
	out := n.narrate(testClusters)
	if !strings.Contains(out["orchestrator"], "unavailable") {
		t.Fatalf("want stub, got %q", out["orchestrator"])
	}
	n.narrate(testClusters)
	if got := r.calls.Load(); got != 1 {
		t.Fatalf("update re-asked a cached rejection: calls = %d", got)
	}
	n.retryFailed = true
	r.output = goodNarrative
	out = n.narrate(testClusters)
	if got := r.calls.Load(); got != 2 || strings.Contains(out["orchestrator"], "unavailable") {
		t.Fatalf("build retry: calls = %d, narrative %q", got, out["orchestrator"])
	}
	if _, failed, _ := cache.Lookup(graph.ClusterHash("orchestrator", testClusters["orchestrator"])); failed {
		t.Error("successful retry should replace the failed entry")
	}
}

func TestNarrate_runnerErrorNotCached(t *testing.T) {
	r := &fakeRunner{err: errors.New("claude not found")}
	n, _ := testNarrator(t, r, false)
	n.narrate(testClusters)
	n.narrate(testClusters)
	if got := r.calls.Load(); got != 2 {
		t.Fatalf("transient runner errors must be retried: calls = %d", got)
	}
}

func TestNarrate_prunesSupersededEntries(t *testing.T) {
	r := &fakeRunner{output: goodNarrative}
	n, cache := testNarrator(t, r, false)
	n.narrate(testClusters)
	oldHash := graph.ClusterHash("orchestrator", testClusters["orchestrator"])

	changed := map[string][]graph.ModuleGraph{
		"orchestrator": {{Module: "internal/orchestrator/api", PackageDoc: "new doc"}, {Module: "internal/orchestrator/store"}},
	}
	n.narrate(changed)
	if _, _, ok := cache.Lookup(oldHash); ok {
		t.Error("superseded cluster hash should be pruned")
	}
}

func TestLoadNarrativeRole_legacyFallsBackToDefault(t *testing.T) {
	golemDir := t.TempDir()
	rolesDir := filepath.Join(golemDir, "roles")
	writeFile(t, filepath.Join(rolesDir, "graph-builder.md"), "Output GRAPH_SUMMARY: lines")
	got := loadNarrativeRole(golemDir, io.Discard)
	if strings.Contains(got, "GRAPH_SUMMARY") || !strings.Contains(got, `"narrative"`) {
		t.Errorf("legacy role not replaced by default:\n%s", got)
	}
	writeFile(t, filepath.Join(rolesDir, "graph-builder.md"), "custom narrative role")
	if got := loadNarrativeRole(golemDir, io.Discard); got != "custom narrative role" {
		t.Errorf("custom role not honoured: %q", got)
	}
}

func TestMergeModuleGraphs(t *testing.T) {
	stored := []graph.ModuleGraph{{Module: "a", PackageDoc: "old"}, {Module: "b"}}
	rebuilt := []graph.ModuleGraph{{Module: "a", PackageDoc: "new"}, {Module: "c"}}
	merged, dirty := mergeModuleGraphs(stored, rebuilt)
	if len(merged) != 3 || !dirty["a"] || !dirty["c"] || dirty["b"] {
		t.Fatalf("merged=%v dirty=%v", merged, dirty)
	}
	for _, g := range merged {
		if g.Module == "a" && g.PackageDoc != "new" {
			t.Error("rebuilt graph should replace stored one")
		}
	}
}

func TestStaleModules_includesDeletedFileDirs(t *testing.T) {
	stale := staleModules([]string{"pkg/a/x.go", "pkg/a/y.go", "README.md"})
	if !stale["pkg/a"] || !stale["."] || len(stale) != 2 {
		t.Errorf("stale = %v", stale)
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
