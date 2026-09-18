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
		{"staging/src/example.com/api/core/v1", "api"},
		{"staging/src/example.com/apimachinery/pkg/util/errors", "apimachinery"},
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

// Oversized single-bucket case: 400 modules pre-tagged as one cluster
// should redistribute across their natural next-level segments.
func TestCoarsenSubsystems_oversizedVendorRoot(t *testing.T) {
	graphs := make([]ModuleGraph, 0, 400)
	orgs := []string{"api", "apimachinery", "client-go", "kubectl"}
	for _, org := range orgs {
		for i := 0; i < 100; i++ {
			graphs = append(graphs, ModuleGraph{
				Module:    "staging/src/example.com/" + org + "/pkg/mod" + itoa(i),
				Subsystem: "staging",
			})
		}
	}
	CoarsenSubsystems(graphs, 150)
	sizes := map[string]int{}
	for _, g := range graphs {
		sizes[g.Subsystem]++
	}
	for _, org := range orgs {
		if sizes[org] != 100 {
			t.Errorf("expected 100 modules for %q, got %d (all: %v)", org, sizes[org], sizes)
		}
	}
	if sizes["staging"] != 0 {
		t.Errorf("expected 0 left tagged \"staging\", got %d", sizes["staging"])
	}
}

// Clusters already under the cap are untouched.
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
			t.Errorf("graph %d: subsystem changed from %q to %q", i, original[i], g.Subsystem)
		}
	}
}

// Shallow paths that can't descend further terminate cleanly.
func TestCoarsenSubsystems_shallowPathsAccepted(t *testing.T) {
	graphs := make([]ModuleGraph, 300)
	for i := range graphs {
		graphs[i] = ModuleGraph{Module: "same" + itoa(i%3), Subsystem: "same"}
	}
	CoarsenSubsystems(graphs, 50)
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
