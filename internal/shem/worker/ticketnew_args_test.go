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
			args := golemTicketNewArgs("t1", "golem/branch", tc.description, "/run/backend.yaml", "")

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
			// The backend document is a flag, so it has to precede the separator.
			cfg := slices.Index(args, "--backend-config")
			if cfg == -1 || cfg > sep {
				t.Errorf("--backend-config is missing or after the separator: %q", args)
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

// The pinned base commit rides in the flag region: it is resolved locally
// from git, never from the untrusted description, and must still precede
// the separator so it is parsed as a flag.
func TestGolemTicketNewArgs_BaseSHAPrecedesSeparator(t *testing.T) {
	sha := "0123456789abcdef0123456789abcdef01234567"
	args := golemTicketNewArgs("t1", "golem/branch", "desc", "/run/backend.yaml", sha)

	sep := slices.Index(args, "--")
	idx := slices.Index(args, "--base-sha")
	if idx == -1 || idx > sep {
		t.Errorf("--base-sha is missing or after the separator: %q", args)
	}
	if args[idx+1] != sha {
		t.Errorf("--base-sha value = %q, want %q", args[idx+1], sha)
	}
}

func TestGolemTicketNewArgs_NoBaseSHAOmitsFlag(t *testing.T) {
	args := golemTicketNewArgs("t1", "golem/branch", "desc", "/run/backend.yaml", "")
	if slices.Contains(args, "--base-sha") {
		t.Errorf("--base-sha present without a value: %q", args)
	}
}

// Acceptance commands travel as repeated --accept flags, before the
// separator, in plan order.
func TestGolemTicketNewArgs_AcceptPrecedesSeparator(t *testing.T) {
	args := golemTicketNewArgs("t1", "b", "desc", "/run/backend.yaml", "", "sh a.sh", "sh b.sh")
	sep := slices.Index(args, "--")
	first := slices.Index(args, "--accept")
	if first == -1 || first > sep || args[first+1] != "sh a.sh" || args[first+3] != "sh b.sh" {
		t.Errorf("args = %q", args)
	}
}
