package config_test

import (
	"testing"

	"github.com/leonp92/golem/internal/orchestrator/config"
)

// ListenPort resolves the same way TriggerLabel does, and for the same
// reason: orchestrator.yaml is bind-mounted read-only in the Docker stack,
// so .env is the only file an operator can actually edit to move the port.
func TestListenPort(t *testing.T) {
	cases := []struct {
		name string
		env  string
		yaml int
		want int
	}{
		{"nothing set at all", "", 0, 8080},
		{"yaml only", "", 9000, 9000},
		{"env wins over yaml", "9100", 9000, 9100},
		{"env alone", "9200", 0, 9200},
		{"blank env falls through to yaml", "", 9000, 9000},
		{"whitespace env is blank", "   ", 9000, 9000},

		// A port that cannot be listened on must not be silently accepted.
		// ":0" binds a RANDOM free port, which starts cleanly and then is
		// not where anybody is looking — the worst of both outcomes.
		{"non-numeric env falls back", "not-a-port", 9000, 9000},
		{"zero env falls back", "0", 9000, 9000},
		{"negative env falls back", "-1", 9000, 9000},
		{"out-of-range env falls back", "70000", 9000, 9000},
		{"out-of-range yaml falls back to default", "", 70000, 8080},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv("GOLEM_PORT", c.env)
			cfg := config.Config{Port: c.yaml}
			if got := cfg.ListenPort(); got != c.want {
				t.Errorf("ListenPort() = %d, want %d (env=%q yaml=%d)", got, c.want, c.env, c.yaml)
			}
		})
	}
}
