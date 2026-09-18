package graph

import (
	"testing"
)

func TestSubsystemForPath(t *testing.T) {
	cases := []struct{ in, want string }{
		{"internal/graph", "graph"},
		{"internal/cli", "cli"},
		{"cmd/golem", "golem"},
		{"src/lib/auth", "auth"},
		{"pkg/orchestrator/api", "orchestrator"},
		{"internal/orchestrator/api", "orchestrator"},
		{"api/handlers", "api"},
		{".", "misc"},
		{"", "misc"},
		{"internal", "misc"},
		{"internal\\graph", "graph"},
		// Vendored-source conventions land on the meaningful segment.
		{"staging/src/k8s.io/api/core/v1", "api"},
		{"staging/src/k8s.io/apimachinery/pkg/util/errors", "apimachinery"},
		{"third_party/protobuf/src/google", "protobuf"},
		{"packages/react-dom/src", "react-dom"},
		{"vendor/github.com/foo/bar", "foo"},
	}
	for _, tc := range cases {
		if got := SubsystemForPath(tc.in); got != tc.want {
			t.Errorf("SubsystemForPath(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestCoarsenSubsystems_kubernetesStagingCase reproduces the k8s
// benchmark finding: 1701 staging/src/k8s.io/... modules collapsed
// into one "staging" cluster. After coarsening, they should redistribute
// across the api/apimachinery/client-go/... subclusters.
func TestCoarsenSubsystems_kubernetesStagingCase(t *testing.T) {
	// Build a synthetic k8s-like staging tree: 4 top-level "orgs" under
	// staging/src/k8s.io/, each with 100 modules — one 400-module cluster
	// under a size cap of 150 should split into four ~100-module clusters.
	graphs := make([]ModuleGraph, 0, 400)
	orgs := []string{"api", "apimachinery", "client-go", "kubectl"}
	for _, org := range orgs {
		for i := 0; i < 100; i++ {
			graphs = append(graphs, ModuleGraph{
				Module:    "staging/src/k8s.io/" + org + "/pkg/mod" + itoa(i),
				Subsystem: SubsystemForPath("staging/src/k8s.io/" + org + "/pkg/mod" + itoa(i)),
			})
		}
	}
	// Before coarsening: SubsystemForPath returns "api"/"apimachinery"/... for
	// each — that's already the meaningful segment because staging/src/k8s.io/
	// are all transparent. So no oversized cluster exists at 100/org. Good.
	sizes := map[string]int{}
	for _, g := range graphs {
		sizes[g.Subsystem]++
	}
	for _, org := range orgs {
		if sizes[org] != 100 {
			t.Fatalf("pre-coarsen: expected 100 modules for %q, got %d", org, sizes[org])
		}
	}

	// Now simulate the pre-fix behavior: force every module's tag to
	// "staging" as the old naive tagger would have done. Coarsening
	// should recover the split.
	for i := range graphs {
		graphs[i].Subsystem = "staging"
	}
	CoarsenSubsystems(graphs, 150)
	sizes = map[string]int{}
	for _, g := range graphs {
		sizes[g.Subsystem]++
	}
	for _, org := range orgs {
		if sizes[org] != 100 {
			t.Errorf("post-coarsen: expected 100 modules for subsystem %q, got %d (all sizes: %v)",
				org, sizes[org], sizes)
		}
	}
	if sizes["staging"] != 0 {
		t.Errorf("post-coarsen: expected 0 modules left tagged \"staging\", got %d", sizes["staging"])
	}
}

// TestCoarsenSubsystems_noOversizedNoop confirms that clusters already
// under the cap are untouched.
func TestCoarsenSubsystems_noOversizedNoop(t *testing.T) {
	graphs := []ModuleGraph{
		{Module: "internal/graph", Subsystem: "graph"},
		{Module: "internal/wiki", Subsystem: "wiki"},
		{Module: "cmd/golem", Subsystem: "golem"},
	}
	original := make([]string, len(graphs))
	for i, g := range graphs {
		original[i] = g.Subsystem
	}
	CoarsenSubsystems(graphs, 200)
	for i, g := range graphs {
		if g.Subsystem != original[i] {
			t.Errorf("graph %d: subsystem changed from %q to %q; expected no-op",
				i, original[i], g.Subsystem)
		}
	}
}

// TestCoarsenSubsystems_shallowPathsAccepted confirms modules whose
// paths are too shallow to descend further keep their current tag
// without infinite-looping.
func TestCoarsenSubsystems_shallowPathsAccepted(t *testing.T) {
	graphs := make([]ModuleGraph, 300)
	for i := range graphs {
		graphs[i] = ModuleGraph{Module: "same" + itoa(i%3), Subsystem: "same"}
	}
	// All modules resolve to the same shallow tag — coarsening should
	// terminate rather than spin.
	CoarsenSubsystems(graphs, 50)
	// No assertion on final size — just that we didn't hang. The test
	// framework's timeout catches infinite loops.
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	buf := []byte{}
	for i > 0 {
		buf = append([]byte{byte('0' + i%10)}, buf...)
		i /= 10
	}
	return string(buf)
}
