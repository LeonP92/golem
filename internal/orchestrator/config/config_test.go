package config_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

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

func TestGitHubConfigDefaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "orchestrator.yaml")
	if err := os.WriteFile(path, []byte("port: 8080\ndb_path: x.db\n"), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := cfg.GitHub.PollIntervalDuration(); got != 15*time.Minute {
		t.Errorf("PollIntervalDuration = %v, want 15m", got)
	}
	if got := cfg.GitHub.DrainIntervalDuration(); got != 20*time.Second {
		t.Errorf("DrainIntervalDuration = %v, want 20s", got)
	}
	if got := cfg.GitHub.ManualSyncCooldownDuration(); got != time.Minute {
		t.Errorf("ManualSyncCooldownDuration = %v, want 1m", got)
	}
	if cfg.GitHub.TokenEnv != "GOLEM_GITHUB_TOKEN" {
		t.Errorf("TokenEnv = %q, want GOLEM_GITHUB_TOKEN", cfg.GitHub.TokenEnv)
	}
}

func TestGitHubConfigOverrides(t *testing.T) {
	path := filepath.Join(t.TempDir(), "orchestrator.yaml")
	body := "port: 8080\ndb_path: x.db\ngithub:\n  poll_interval: 5m\n  drain_interval: 1s\n  manual_sync_cooldown: 10s\n  token_env: OTHER_TOKEN\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := cfg.GitHub.PollIntervalDuration(); got != 5*time.Minute {
		t.Errorf("PollIntervalDuration = %v, want 5m", got)
	}
	if got := cfg.GitHub.DrainIntervalDuration(); got != time.Second {
		t.Errorf("DrainIntervalDuration = %v, want 1s", got)
	}
	if cfg.GitHub.TokenEnv != "OTHER_TOKEN" {
		t.Errorf("TokenEnv = %q, want OTHER_TOKEN", cfg.GitHub.TokenEnv)
	}
}

// TestGitHubConfigInvalidDurationFallsBack covers fix-round-1's judgment-call
// item: parseDurationOr must not silently swallow an unparseable or
// non-positive duration string — it logs the bad value and falls back to the
// documented default, which is the behavior asserted here. (The log line
// itself isn't captured; this pins the observable fallback value.)
func TestGitHubConfigInvalidDurationFallsBack(t *testing.T) {
	tests := []struct {
		name string
		body string
		want time.Duration
	}{
		{
			name: "unparseable value falls back to the 15m default",
			body: "port: 8080\ndb_path: x.db\ngithub:\n  poll_interval: 15mn\n",
			want: 15 * time.Minute,
		},
		{
			name: "non-positive value falls back to the 15m default",
			body: "port: 8080\ndb_path: x.db\ngithub:\n  poll_interval: -5m\n",
			want: 15 * time.Minute,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "orchestrator.yaml")
			if err := os.WriteFile(path, []byte(tt.body), 0o600); err != nil {
				t.Fatalf("write config: %v", err)
			}
			cfg, err := config.Load(path)
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if got := cfg.GitHub.PollIntervalDuration(); got != tt.want {
				t.Errorf("PollIntervalDuration = %v, want %v", got, tt.want)
			}
		})
	}
}
