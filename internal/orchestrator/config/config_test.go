package config_test

import (
	"os"
	"testing"

	"github.com/leonp92/golem/internal/orchestrator/config"
)

func TestLoad(t *testing.T) {
	f, _ := os.CreateTemp("", "orch*.yaml")
	f.WriteString("port: 9090\ndb_path: ./test.db\nsession_secret: abc123\n")
	f.Close()
	defer os.Remove(f.Name())

	cfg, err := config.Load(f.Name())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Port != 9090 {
		t.Errorf("port: got %d, want 9090", cfg.Port)
	}
	if cfg.DBPath != "./test.db" {
		t.Errorf("db_path: got %q", cfg.DBPath)
	}
}
