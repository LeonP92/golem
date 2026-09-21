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
		{"negative env falls back", "-2", 5, 5},

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
