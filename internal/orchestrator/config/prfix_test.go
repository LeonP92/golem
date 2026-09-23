package config_test

import (
	"testing"

	"github.com/leonp92/golem/internal/orchestrator/config"
)

// The cap bounds how many times the shem will try to fix a pull request
// before handing it to a human. Without one, a failure the agent cannot fix
// becomes an unbounded fix/push/fail loop: every push triggers CI, every red
// CI triggers another agent run, and nothing ever stops it.
func TestPRFixAttempts(t *testing.T) {
	cases := []struct {
		name string
		env  string
		yaml int
		want int
	}{
		{"default", "", 0, 3},
		{"yaml", "", 5, 5},
		{"env wins", "7", 5, 7},
		{"blank env falls through", "", 5, 5},
		{"whitespace env", "  ", 5, 5},
		{"non-numeric env falls back", "lots", 5, 5},

		// -1 is unlimited: keep trying for as long as the pull request is
		// open. Any OTHER negative is a typo and must not silently turn
		// the cap off — that is the one mistake whose cost is unbounded.
		{"minus one is unlimited", "-1", 5, config.UnlimitedPRFixAttempts},
		{"minus one beats yaml", "-1", 0, config.UnlimitedPRFixAttempts},
		{"other negatives fall back", "-2", 5, 5},
		{"other negatives fall back to default", "-7", 0, 3},

		// Zero is meaningful and must be honoured, not treated as unset:
		// it is how an operator says "never auto-fix, always tell me".
		{"zero env disables auto-fix", "0", 5, 0},
		{"zero yaml disables auto-fix", "", 0, 3},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv("GOLEM_PR_FIX_ATTEMPTS", c.env)
			g := config.GitHubConfig{PRFixAttempts: c.yaml}
			if got := g.MaxPRFixAttempts(); got != c.want {
				t.Errorf("MaxPRFixAttempts() = %d, want %d (env=%q yaml=%d)", got, c.want, c.env, c.yaml)
			}
		})
	}
}

// The sentinel has to be a value no real cap could be, and callers have to
// be able to test for it without knowing the number.
func TestUnlimitedSentinel(t *testing.T) {
	if config.UnlimitedPRFixAttempts >= 0 {
		t.Fatalf("UnlimitedPRFixAttempts = %d; a non-negative sentinel is "+
			"indistinguishable from a real cap", config.UnlimitedPRFixAttempts)
	}
}
