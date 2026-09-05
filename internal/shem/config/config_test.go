package config_test

import (
	"os"
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
