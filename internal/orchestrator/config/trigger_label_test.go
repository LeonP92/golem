package config_test

import (
	"testing"

	"github.com/leonp92/golem/internal/orchestrator/config"
	"github.com/leonp92/golem/internal/orchestrator/ghsync"
)

// The trigger label used to be the literal "golem" in two places in the UI,
// so changing it meant editing every repository's settings by hand. It is now
// a default an operator sets once, and the resolution order is the contract:
// environment first (that is where a Docker deployment sets it, and
// orchestrator.yaml is bind-mounted read-only), then the config file, then
// "golem".
func TestTriggerLabelResolutionOrder(t *testing.T) {
	tests := []struct {
		name string
		env  string
		yaml string
		want string
	}{
		{"nothing set", "", "", "golem"},
		{"config file only", "", "triage", "triage"},
		{"environment only", "needs-golem", "", "needs-golem"},
		{"environment wins over the config file", "needs-golem", "triage", "needs-golem"},
		{"whitespace in the environment is not a value", "   ", "triage", "triage"},
		{"a padded environment value is trimmed", "  triage  ", "", "triage"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("GOLEM_GITHUB_LABEL", tt.env)
			g := config.GitHubConfig{DefaultLabel: tt.yaml}
			if got := g.TriggerLabel(); got != tt.want {
				t.Errorf("TriggerLabel() = %q, want %q", got, tt.want)
			}
		})
	}
}

// config duplicates the default rather than importing ghsync, to keep it free
// of a dependency on the sync engine. This is what stops the two drifting.
func TestDefaultTriggerLabelMatchesGhsync(t *testing.T) {
	t.Setenv("GOLEM_GITHUB_LABEL", "")
	if got := (config.GitHubConfig{}).TriggerLabel(); got != ghsync.DefaultTriggerLabel {
		t.Errorf("config default = %q, ghsync.DefaultTriggerLabel = %q; they must agree",
			got, ghsync.DefaultTriggerLabel)
	}
}

// A trigger label inside golem:* is self-defeating — applyPhaseLabel removes
// every golem:* label it does not currently want, so the trigger would be
// stripped on the first phase change. The near-misses must keep working.
func TestValidateTriggerLabel(t *testing.T) {
	tests := []struct {
		label  string
		reject bool
	}{
		{"golem", false},
		{"triage", false},
		{"needs-golem", false},
		{"golem-adjacent", false},
		{"golemite", false},
		{"Golem", false},
		{"team:golem", false},
		{"golem:", true},
		{"golem:triage", true},
		{"golem:implement", true},
	}
	for _, tt := range tests {
		t.Run(tt.label, func(t *testing.T) {
			err := ghsync.ValidateTriggerLabel(tt.label)
			if tt.reject && err == nil {
				t.Errorf("ValidateTriggerLabel(%q) = nil, want an error", tt.label)
			}
			if !tt.reject && err != nil {
				t.Errorf("ValidateTriggerLabel(%q) = %v, want nil", tt.label, err)
			}
		})
	}
}
