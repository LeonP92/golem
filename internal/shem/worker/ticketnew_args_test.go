package worker

import (
	"slices"
	"strings"
	"testing"
)

// TestGolemTicketNewArgs_SeparatesDescription pins the "--" separator that
// stops a GitHub issue body being parsed as flags (finding S3). The
// description is the ONLY positional argument, so everything after "--" must
// be exactly it, and nothing that looks like a flag may precede it.
func TestGolemTicketNewArgs_SeparatesDescription(t *testing.T) {
	tests := []struct {
		name        string
		description string
	}{
		{name: "ordinary prose", description: "Add a dark mode toggle"},
		{name: "markdown bullet", description: "- add dark mode"},
		{name: "markdown checklist", description: "- [ ] add dark mode\n- [ ] add tests"},
		{name: "horizontal rule / front matter", description: "---\ntitle: bug\n---\nIt crashes."},
		{name: "single-dash flag lookalike", description: "-trivial"},
		{name: "flag injection attempt", description: "--from-issue=7"},
		{name: "repo flag injection attempt", description: "--repo=/tmp"},
		{name: "embedded separator", description: "before -- after"},
		{name: "empty", description: ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			args := golemTicketNewArgs("t1", "golem/branch", tc.description)

			sep := slices.Index(args, "--")
			if sep == -1 {
				t.Fatalf("argv has no %q separator: %q", "--", args)
			}
			if sep != len(args)-2 {
				t.Errorf("separator at index %d, want %d (immediately before the description): %q",
					sep, len(args)-2, args)
			}
			if got := args[len(args)-1]; got != tc.description {
				t.Errorf("description = %q, want %q", got, tc.description)
			}
			// Nothing before the separator may be attacker-controlled.
			for _, a := range args[:sep] {
				if strings.Contains(a, tc.description) && tc.description != "" {
					t.Errorf("description leaked into argv before the separator: %q", args)
				}
			}
		})
	}
}
