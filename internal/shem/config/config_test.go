package config_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/leonp92/golem/internal/shem/config"
)

func TestLoad_NormalizesRepos(t *testing.T) {
	f, err := os.CreateTemp("", "shem*.yaml")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.WriteString(`
orchestrator: https://golem.example.com
api_key: secret
name: node-a
repos:
  - path: /repos/myapp
    remote: https://github.com/org/myapp.git
`)
	f.Close()
	defer os.Remove(f.Name())

	cfg, err := config.Load(f.Name())
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Repos[0].NormalizedRemote != "https://github.com/org/myapp" {
		t.Errorf("got %q", cfg.Repos[0].NormalizedRemote)
	}
}

func TestLoad_Fields(t *testing.T) {
	// Load honours these as overrides, so a shem's own environment would
	// otherwise decide what this test reads back.
	t.Setenv("GOLEM_SHEM_API_KEY", "")
	t.Setenv("GOLEM_SHEM_NAME", "")

	f, err := os.CreateTemp("", "shem*.yaml")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.WriteString(`
orchestrator: https://golem.example.com
api_key: mysecret
name: my-node
repos:
  - path: /repos/proj
    remote: https://github.com/org/proj
`)
	f.Close()
	defer os.Remove(f.Name())

	cfg, err := config.Load(f.Name())
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Orchestrator != "https://golem.example.com" {
		t.Errorf("orchestrator: got %q", cfg.Orchestrator)
	}
	if cfg.APIKey != "mysecret" {
		t.Errorf("api_key: got %q", cfg.APIKey)
	}
	if cfg.Name != "my-node" {
		t.Errorf("name: got %q", cfg.Name)
	}
	if len(cfg.Repos) != 1 {
		t.Fatalf("repos: expected 1, got %d", len(cfg.Repos))
	}
	if cfg.Repos[0].Path != "/repos/proj" {
		t.Errorf("repo path: got %q", cfg.Repos[0].Path)
	}
}

func TestLoad_MissingFile(t *testing.T) {
	_, err := config.Load("/nonexistent/path/shem.yaml")
	if err == nil {
		t.Error("expected error for missing file")
	}
}

func TestAttentionPolicy(t *testing.T) {
	for _, tc := range []struct {
		name, fields string
		on           bool
		max          int
	}{
		{"default", "", true, 3},
		{"opt out", "    auto_retry: false\n", false, 3},
		{"custom max", "    attention_fix_attempts: 7\n", true, 7},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "shem.yaml")
			if err := os.WriteFile(path, []byte("repos:\n  - path: /repo\n    remote: local://repo\n"+tc.fields), 0644); err != nil {
				t.Fatal(err)
			}
			cfg, err := config.Load(path)
			if err != nil {
				t.Fatal(err)
			}
			on, max := cfg.AttentionPolicy(cfg.Repos[0].NormalizedRemote)
			if on != tc.on || max != tc.max {
				t.Fatalf("policy %v %d", on, max)
			}
		})
	}
	path := filepath.Join(t.TempDir(), "shem.yaml")
	os.WriteFile(path, []byte("repos:\n  - remote: local://repo\n    attention_fix_attempts: -1\n"), 0644)
	if _, err := config.Load(path); err == nil {
		t.Fatal("negative retry max accepted")
	}
}
