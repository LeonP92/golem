package graph

import "testing"

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
	}
	for _, tc := range cases {
		if got := SubsystemForPath(tc.in); got != tc.want {
			t.Errorf("SubsystemForPath(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
