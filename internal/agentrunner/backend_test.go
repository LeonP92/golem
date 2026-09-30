package agentrunner

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/leonp92/golem/internal/models"
)

func writeDoc(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "backend.yaml")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestWriteThenLoadRoundTrips(t *testing.T) {
	b := ResolveBackend(nil)
	b.Command = "my-claude"
	b.ExtraArgs = []string{"--add-dir", "/opt/toolchains"}
	b.Env = map[string]string{"ANTHROPIC_BASE_URL": "https://proxy.example"}
	path := filepath.Join(t.TempDir(), "backend.yaml")
	if err := b.Write(path); err != nil {
		t.Fatalf("Write: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o644 { // no Unix modes on Windows
		t.Errorf("mode = %v, want 0644", info.Mode().Perm())
	}
	got, err := LoadBackendConfig(path)
	if err != nil {
		t.Fatalf("LoadBackendConfig: %v", err)
	}
	if !reflect.DeepEqual(got, b) {
		t.Errorf("round trip changed the block:\ngot  %+v\nwant %+v", got, b)
	}

	doc, err := b.MarshalDocument()
	if err != nil {
		t.Fatalf("MarshalDocument: %v", err)
	}
	onDisk, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(doc, onDisk) {
		t.Error("MarshalDocument and Write disagree")
	}
}

func TestClaudeCodeDefaultCatalog(t *testing.T) {
	def := ResolveBackend(nil)
	if err := def.Validate(); err != nil {
		t.Fatalf("the default catalog does not validate: %v", err)
	}
	if len(def.Models) != 3 {
		t.Errorf("the default catalog has %d models, want 3", len(def.Models))
	}
	for _, st := range models.Stages {
		if _, ok := def.StageDefaults[st]; !ok {
			t.Errorf("the default catalog has no stage_defaults entry for %q", st)
		}
	}
	if len(def.StageDefaults) != len(models.Stages) {
		t.Errorf("the default catalog has %d stage_defaults, want %d", len(def.StageDefaults), len(models.Stages))
	}
}

func TestResolveBackend(t *testing.T) {
	def := ClaudeCode{}.DefaultCatalog()
	load := func(t *testing.T, block string) BackendConfig {
		t.Helper()
		got, err := LoadBackendConfig(writeDoc(t, "backend:\n"+block))
		if err != nil {
			t.Fatalf("LoadBackendConfig: %v", err)
		}
		return got
	}

	t.Run("nil is the default adapter with its catalog", func(t *testing.T) {
		got := ResolveBackend(nil)
		if got.Adapter != DefaultAdapter || !reflect.DeepEqual(got.Catalog, def) {
			t.Errorf("got %+v, want %s with its default catalog", got, DefaultAdapter)
		}
	})

	t.Run("no catalog keys gets the default catalog", func(t *testing.T) {
		got := load(t, "  command: my-claude\n")
		if !reflect.DeepEqual(got.Catalog, def) {
			t.Errorf("catalog = %+v, want the default", got.Catalog)
		}
	})

	t.Run("declared models are used as written", func(t *testing.T) {
		got := load(t, "  tiers: [fast]\n  models:\n    - {id: only, tier: fast}\n")
		if len(got.Models) != 1 || got.Models[0].ID != "only" || len(got.StageDefaults) != 0 {
			t.Errorf("catalog = %+v, want just 'only' and no stage defaults", got.Catalog)
		}
	})

	t.Run("an explicit supports_selection false is kept", func(t *testing.T) {
		got := load(t, "  supports_selection: false\n")
		if got.SupportsSelection || !reflect.DeepEqual(got.Models, def.Models) {
			t.Errorf("got selection=%v models=%v, want false over the default models", got.SupportsSelection, got.Models)
		}
	})

	t.Run("declared stage defaults override the default ones", func(t *testing.T) {
		got := load(t, "  stage_defaults: {review: small}\n")
		if got.StageDefaults["review"] != "small" || len(got.StageDefaults) != 1 {
			t.Errorf("stage defaults = %v, want only review: small", got.StageDefaults)
		}
	})

	t.Run("tiers the default models do not use fail validation", func(t *testing.T) {
		_, err := LoadBackendConfig(writeDoc(t, "backend:\n  tiers: [fast, deep]\n"))
		if err == nil {
			t.Error("custom tiers over the default models loaded without error")
		}
	})

	t.Run("an unknown key fails to load", func(t *testing.T) {
		_, err := LoadBackendConfig(writeDoc(t, "backend:\n  extra-args: [--x]\n"))
		if err == nil || !strings.Contains(err.Error(), "extra-args") {
			t.Errorf("err = %v, want one naming extra-args", err)
		}
	})
}

func TestLoadBackendConfigReadsOnlyTheBackendKey(t *testing.T) {
	t.Run("a shem.yaml", func(t *testing.T) {
		path := writeDoc(t, `
orchestrator: https://golem.example.com
api_key: secret
backend:
  command: my-claude
`)
		got, err := LoadBackendConfig(path)
		if err != nil {
			t.Fatalf("LoadBackendConfig: %v", err)
		}
		if got.Command != "my-claude" {
			t.Errorf("command = %q, want my-claude", got.Command)
		}
	})

	t.Run("no backend key gives the default", func(t *testing.T) {
		path := writeDoc(t, "orchestrator: https://golem.example.com\n")
		got, err := LoadBackendConfig(path)
		if err != nil {
			t.Fatalf("LoadBackendConfig: %v", err)
		}
		if !reflect.DeepEqual(got, ResolveBackend(nil)) {
			t.Errorf("got %+v, want the default", got)
		}
	})
}

func TestBackendConfigValidate(t *testing.T) {
	tests := []struct {
		name    string
		in      BackendConfig
		wantErr bool
	}{
		{"unknown adapter", BackendConfig{Adapter: "nonesuch"}, true},
		{"extra_args --print", BackendConfig{Adapter: "claude-code", ExtraArgs: []string{"--print"}}, true},
		{"extra_args -p", BackendConfig{Adapter: "claude-code", ExtraArgs: []string{"-p"}}, true},
		{"extra_args --model", BackendConfig{Adapter: "claude-code", ExtraArgs: []string{"--model"}}, true},
		{"extra_args --model=opus", BackendConfig{Adapter: "claude-code", ExtraArgs: []string{"--model=opus"}}, true},
		{"model tier not in tiers", BackendConfig{Adapter: "claude-code",
			Catalog: models.Catalog{Models: []models.Model{{ID: "a", Tier: "huge"}}}}, true},
		{"stage_defaults key not a stage", BackendConfig{Adapter: "claude-code",
			Catalog: models.Catalog{Tiers: []models.Tier{"small"}, StageDefaults: map[models.Stage]models.Tier{"nope": "small"}}}, true},
		{"stage_defaults value not a tier", BackendConfig{Adapter: "claude-code",
			Catalog: models.Catalog{Tiers: []models.Tier{"small"}, StageDefaults: map[models.Stage]models.Tier{models.StagePlan: "huge"}}}, true},
		{"duplicate model id", BackendConfig{Adapter: "claude-code",
			Catalog: models.Catalog{Models: []models.Model{{ID: "a"}, {ID: "a"}}}}, true},
		{"harmless extra_args", BackendConfig{Adapter: "claude-code",
			ExtraArgs: []string{"--add-dir", "/opt/toolchains"}}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.in.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

// The operator's env reaches the agent as declared, except golem's own
// variables, which are refused at startup rather than silently dropped.
func TestBackendEnvRefusesGolemOwnNames(t *testing.T) {
	b := ResolveBackend(nil)
	b.Env = map[string]string{"MY_TOOL_TOKEN": "x"}
	if err := b.Validate(); err != nil {
		t.Errorf("Validate with an operator variable: %v", err)
	}
	b.Env = map[string]string{"GOLEM_SHEM_API_KEY": "x"}
	if err := b.Validate(); err == nil || !strings.Contains(err.Error(), "GOLEM_SHEM_API_KEY") {
		t.Errorf("Validate = %v, want an error naming GOLEM_SHEM_API_KEY", err)
	}
}
