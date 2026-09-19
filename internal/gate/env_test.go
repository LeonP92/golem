package gate

import (
	"strings"
	"testing"

	"github.com/leonp92/golem/internal/config"
)

// A gate command is not Golem's command. It is a line of shell out of
// .golem/config.yaml, a file that lives INSIDE the repository an agent works
// in and that an agent can therefore edit. Run exec's it with `sh -c`, so
// whatever environment this process holds is the environment that shell holds.
//
// That was the single path left making the claim "the agent never sees
// GOLEM_*" false. Rounds 3 and 3b scoped both `claude` exec sites; a gate
// command is executed later, by whichever process next runs `golem ticket
// review` on that repository — and on a co-located single-box deployment that
// process has GOLEM_GITHUB_TOKEN. A branch that documents a property it does
// not have is worse than one that documents a narrower property honestly.
//
// The allow-list is the right one by the same argument that justifies it for
// the agent: gate commands are build and test commands, run in the same
// repository and by the same toolchain the agent uses, so what suffices for
// the agent's own `make test` suffices here. GOLEM_AGENT_ENV remains the
// escape hatch for a gate that genuinely needs more, and it cannot re-add the
// GOLEM_ namespace.
func TestRunDoesNotLeakGolemEnvironmentToGateCommands(t *testing.T) {
	tests := []struct {
		name    string
		env     map[string]string
		command string
		// want is the value the gate command must observe.
		want string
	}{
		{
			name:    "the github push credential is not visible",
			env:     map[string]string{"GOLEM_GITHUB_TOKEN": "ghp_must_not_reach_a_gate_command"},
			command: `printf 'SAW[%s]' "$GOLEM_GITHUB_TOKEN"`,
			want:    "SAW[]",
		},
		{
			name:    "nor is the shem api key",
			env:     map[string]string{"GOLEM_SHEM_API_KEY": "must-not-reach-a-gate-command"},
			command: `printf 'SAW[%s]' "$GOLEM_SHEM_API_KEY"`,
			want:    "SAW[]",
		},
		{
			name:    "nor the orchestrator database path",
			env:     map[string]string{"ORCHESTRATOR_DB": "/data/orchestrator.db"},
			command: `printf 'SAW[%s]' "$ORCHESTRATOR_DB"`,
			want:    "SAW[]",
		},
		{
			name:    "a build variable the toolchain needs still is",
			env:     map[string]string{"GOFLAGS": "-mod=mod"},
			command: `printf 'SAW[%s]' "$GOFLAGS"`,
			want:    "SAW[-mod=mod]",
		},
		{
			name:    "PATH still is, or nothing could run at all",
			command: `test -n "$PATH" && printf 'SAW[set]'`,
			want:    "SAW[set]",
		},
		{
			name: "the operator can widen it for a gate that needs more",
			env: map[string]string{
				"GOLEM_AGENT_ENV": "MY_BUILD_SECRET",
				"MY_BUILD_SECRET": "needed-by-the-build",
			},
			command: `printf 'SAW[%s]' "$MY_BUILD_SECRET"`,
			want:    "SAW[needed-by-the-build]",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for k, v := range tt.env {
				t.Setenv(k, v)
			}
			result, err := Run(t.TempDir(), config.GateConfig{Commands: []string{tt.command}})
			if err != nil {
				t.Fatalf("Run: %v", err)
			}
			if !result.Passed {
				t.Fatalf("gate command failed: %s", result.Output)
			}
			if !strings.Contains(result.Output, tt.want) {
				t.Errorf("the gate command observed %q, want it to contain %q",
					result.Output, tt.want)
			}
		})
	}
}
