package config_test

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/leonp92/golem/internal/agentrunner"
	"github.com/leonp92/golem/internal/shem/config"
)

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "shem.yaml")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

const minimalConfig = `
orchestrator: https://golem.example.com
api_key: secret
name: node-a
`

func TestLoadWithNoBackendBlockGivesTheDefault(t *testing.T) {
	cfg, err := config.Load(writeConfig(t, minimalConfig))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Backend == nil {
		t.Fatal("Backend is nil")
	}
	if cfg.Backend.Adapter != "claude-code" {
		t.Errorf("adapter = %q, want claude-code", cfg.Backend.Adapter)
	}
	if !reflect.DeepEqual(cfg.Backend.Catalog, agentrunner.ResolveBackend(nil).Catalog) {
		t.Errorf("catalog = %+v, want the default", cfg.Backend.Catalog)
	}
}

func TestLoadKeepsADeclaredCatalogAsWritten(t *testing.T) {
	cfg, err := config.Load(writeConfig(t, minimalConfig+`
backend:
  models:
    - {id: only, label: "The one model"}
`))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.Backend.Models) != 1 || cfg.Backend.Models[0].ID != "only" {
		t.Errorf("models = %+v, want just 'only'", cfg.Backend.Models)
	}
	if len(cfg.Backend.StageDefaults) != 0 {
		t.Errorf("StageDefaults = %v, want empty", cfg.Backend.StageDefaults)
	}
}

func TestLoadRejectsABadBackendBlock(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{"unknown adapter", "backend:\n  adapter: nonesuch\n"},
		{"model tier not in tiers", "backend:\n  tiers: [small]\n  models:\n    - {id: a, tier: huge}\n"},
		{"stage_defaults key not a stage", "backend:\n  tiers: [small]\n  models:\n    - {id: a, tier: small}\n  stage_defaults:\n    nope: small\n"},
		{"stage_defaults value not a tier", "backend:\n  tiers: [small]\n  models:\n    - {id: a, tier: small}\n  stage_defaults:\n    plan: huge\n"},
		{"duplicate model id", "backend:\n  models:\n    - {id: a}\n    - {id: a}\n"},
		{"extra_args --print", "backend:\n  extra_args: [\"--print\"]\n"},
		{"extra_args --model=opus", "backend:\n  extra_args: [\"--model=opus\"]\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := config.Load(writeConfig(t, minimalConfig+tt.body))
			if err == nil {
				t.Fatal("the bad block was accepted")
			}
		})
	}
}
