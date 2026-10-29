package gate

import (
	"testing"

	"github.com/leonp92/golem/internal/config"
	"github.com/leonp92/golem/internal/evidence"
)

func TestEnvironmentEvidence(t *testing.T) {
	for _, tc := range []struct {
		cfg  config.GateConfig
		kind string
		exit int
	}{
		{config.GateConfig{Commands: []string{"missing_synthetic_golem_tool"}}, "environment", 127},
		{config.GateConfig{EnvironmentChecks: []string{"echo DB_unavailable; exit 3"}}, "environment", 3},
		{config.GateConfig{Commands: []string{"echo Unauthorized_quota_assertion; exit 1"}}, "code", 1},
	} {
		r, err := Run(t.TempDir(), tc.cfg)
		if err != nil || r.Passed || r.Failure == nil || r.Failure.Kind != tc.kind || r.Failure.ExitCode != tc.exit {
			t.Fatalf("%+v %v", r, err)
		}
		if EnvironmentOutput(r.Output) != (tc.kind == "environment") {
			t.Fatal(r.Output)
		}
	}
}

func TestEnvironmentOutputOnlyAtStartOrAfterReviewWrapper(t *testing.T) {
	m := evidence.EnvironmentMarker
	for text, want := range map[string]bool{
		m + " x": true,
		"review gate could not run: exit 1\n" + m: true,
		"test output\n" + m + " x":                false,
		"want \"" + m + "\"":                      false,
	} {
		if EnvironmentOutput(text) != want {
			t.Errorf("%q: want %v", text, want)
		}
	}
}
