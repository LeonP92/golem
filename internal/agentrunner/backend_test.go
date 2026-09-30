package agentrunner

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
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
	b := SeedClaudeCode()
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
	if info.Mode().Perm() != 0o644 {
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

func TestSeedClaudeCode(t *testing.T) {
	seed := SeedClaudeCode()
	if err := seed.Validate(); err != nil {
		t.Fatalf("the seed does not validate: %v", err)
	}
	if len(seed.Models) != 3 {
		t.Errorf("seed has %d models, want 3", len(seed.Models))
	}
	for _, st := range models.Stages {
		if _, ok := seed.StageDefaults[st]; !ok {
			t.Errorf("the seed has no stage_defaults entry for %q", st)
		}
	}
	if len(seed.StageDefaults) != len(models.Stages) {
		t.Errorf("seed has %d stage_defaults, want %d", len(seed.StageDefaults), len(models.Stages))
	}
}

func TestResolveBackend(t *testing.T) {
	seed := SeedClaudeCode()

	if got := ResolveBackend(nil); !reflect.DeepEqual(got, seed) {
		t.Error("a nil block did not resolve to the seed")
	}

	t.Run("no models gets the seed catalog whole", func(t *testing.T) {
		got := ResolveBackend(&BackendConfig{Command: "my-claude"})
		if !reflect.DeepEqual(got.Catalog, seed.Catalog) {
			t.Errorf("catalog = %+v, want the seed's", got.Catalog)
		}
		if got.Adapter != "claude-code" {
			t.Errorf("adapter = %q, want claude-code", got.Adapter)
		}
	})

	t.Run("a declared catalog is kept as written", func(t *testing.T) {
		in := &BackendConfig{Catalog: models.Catalog{Models: []models.Model{{ID: "only"}}}}
		got := ResolveBackend(in)
		if len(got.Models) != 1 || got.Models[0].ID != "only" {
			t.Errorf("models = %+v, want just 'only'", got.Models)
		}
		if len(got.StageDefaults) != 0 {
			t.Errorf("StageDefaults = %v, want empty", got.StageDefaults)
		}
	})

	t.Run("another adapter with no models keeps none", func(t *testing.T) {
		got := ResolveBackend(&BackendConfig{Adapter: "codex"})
		if len(got.Models) != 0 {
			t.Errorf("models = %+v, want none", got.Models)
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

	t.Run("no backend key gives the seed", func(t *testing.T) {
		path := writeDoc(t, "orchestrator: https://golem.example.com\n")
		got, err := LoadBackendConfig(path)
		if err != nil {
			t.Fatalf("LoadBackendConfig: %v", err)
		}
		if !reflect.DeepEqual(got, SeedClaudeCode()) {
			t.Errorf("got %+v, want the seed", got)
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
