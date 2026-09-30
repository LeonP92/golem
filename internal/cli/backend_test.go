package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/leonp92/golem/internal/agentrunner"
	"github.com/leonp92/golem/internal/config"
)

func writeBackendDoc(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "backend.yaml")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestNewRunnerSelectsTheAdapter(t *testing.T) {
	doc := writeBackendDoc(t, "backend:\n  adapter: claude-code\n  command: my-claude\n")

	tests := []struct {
		name          string
		src           RunnerSource
		wantName      string
		wantErrSubstr string
	}{
		{
			name:     "backend-config names the adapter",
			src:      RunnerSource{BackendConfig: doc},
			wantName: "claude-code",
		},
		{
			name:     "backend alone",
			src:      RunnerSource{Backend: "claude-code", Config: &config.Config{}},
			wantName: "claude-code",
		},
		{
			name:     "both agreeing",
			src:      RunnerSource{Backend: "claude-code", BackendConfig: doc},
			wantName: "claude-code",
		},
		{
			name:          "both disagreeing",
			src:           RunnerSource{Backend: "codex", BackendConfig: doc},
			wantErrSubstr: "codex",
		},
		{
			name:     "neither falls back to the repo config",
			src:      RunnerSource{Config: &config.Config{Backend: "claude-code"}},
			wantName: "claude-code",
		},
		{
			name:          "unreadable backend-config",
			src:           RunnerSource{BackendConfig: filepath.Join(t.TempDir(), "absent.yaml")},
			wantErrSubstr: "absent.yaml",
		},
		{
			name:          "unknown backend name",
			src:           RunnerSource{Backend: "not-a-real-backend", Config: &config.Config{}},
			wantErrSubstr: "not-a-real-backend",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runner, err := NewRunner(tt.src)
			if tt.wantErrSubstr != "" {
				if err == nil {
					t.Fatalf("want an error naming %q, got %T", tt.wantErrSubstr, runner)
				}
				if !strings.Contains(err.Error(), tt.wantErrSubstr) {
					t.Errorf("error = %v, want it to name %q", err, tt.wantErrSubstr)
				}
				return
			}
			if err != nil {
				t.Fatalf("NewRunner: %v", err)
			}
			if runner.Name() != tt.wantName {
				t.Errorf("adapter = %q, want %q", runner.Name(), tt.wantName)
			}
		})
	}
}

func TestNewRunnerDisagreementNamesBoth(t *testing.T) {
	doc := writeBackendDoc(t, "backend:\n  adapter: claude-code\n")
	_, err := NewRunner(RunnerSource{Backend: "codex", BackendConfig: doc})
	if err == nil {
		t.Fatal("expected an error")
	}
	for _, want := range []string{"codex", "claude-code"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %v does not name %q", err, want)
		}
	}
}

// The registry is what NewRunner resolves through, so an adapter absent from
// it is an error rather than a default.
func TestNewRunnerUsesTheRegistry(t *testing.T) {
	if len(agentrunner.Names()) == 0 {
		t.Fatal("no adapter is registered")
	}
}
