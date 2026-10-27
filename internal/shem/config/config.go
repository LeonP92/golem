package config

import (
	"fmt"
	"os"
	"time"

	"github.com/leonp92/golem/internal/agentrunner"
	"github.com/leonp92/golem/internal/orchestrator/urlnorm"
	"gopkg.in/yaml.v3"
)

// RepoConfig holds configuration for a single git repository managed by this shem.
type RepoConfig struct {
	Path             string        `yaml:"path"`
	WorktreeSetup    []string      `yaml:"worktree_setup"`
	SetupTimeout     time.Duration `yaml:"setup_timeout"`
	Remote           string        `yaml:"remote"`
	NormalizedRemote string        `yaml:"-"`
	// AutoRetry defaults on. This host-owned setting cannot be changed by a ticket.
	AutoRetry *bool `yaml:"auto_retry"`
	// AttentionFixAttempts caps diagnosis/repair dispatches for a ticket; nil means 3.
	AttentionFixAttempts *int `yaml:"attention_fix_attempts"`
}

// Config holds all shem configuration loaded from shem.yaml.
type Config struct {
	Orchestrator string       `yaml:"orchestrator"`
	APIKey       string       `yaml:"api_key"`
	Name         string       `yaml:"name"`
	Repos        []RepoConfig `yaml:"repos"`
	// NoPush disables git push to remote after ticket completion.
	// Set to true for local testing when no remote is configured.
	NoPush bool `yaml:"no_push"`
	// MaxConcurrent is the maximum number of tickets to run in parallel.
	// Defaults to 1 if unset or zero.
	MaxConcurrent int `yaml:"max_concurrent"`
	// Backend is the single backend this shem runs; absent means the default
	// adapter with its default catalog.
	Backend *agentrunner.BackendConfig `yaml:"backend"`
}

// Load reads and parses the YAML config file at path, normalizing repo remote URLs.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}
	for i := range cfg.Repos {
		if cfg.Repos[i].SetupTimeout < 0 {
			return nil, fmt.Errorf("repos[%d].setup_timeout must not be negative", i)
		}
		if n := cfg.Repos[i].AttentionFixAttempts; n != nil && *n < 1 {
			return nil, fmt.Errorf("repos[%d].attention_fix_attempts must be positive", i)
		}
		cfg.Repos[i].NormalizedRemote = urlnorm.Normalize(cfg.Repos[i].Remote)
	}
	b := agentrunner.ResolveBackend(cfg.Backend)
	if err := b.Validate(); err != nil {
		return nil, fmt.Errorf("backend: %w", err)
	}
	cfg.Backend = &b
	// GOLEM_SHEM_API_KEY overrides the config file key so Docker deployments
	// can inject the key via environment without editing shem.yaml.
	if key := os.Getenv("GOLEM_SHEM_API_KEY"); key != "" {
		cfg.APIKey = key
	}
	if name := os.Getenv("GOLEM_SHEM_NAME"); name != "" {
		cfg.Name = name
	}
	return &cfg, nil
}

// AttentionPolicy returns the host-owned recovery policy for a repository.
// Load rejects attempts below 1, so the cap is always positive here.
func (c *Config) AttentionPolicy(remote string) (bool, int) {
	for _, r := range c.Repos {
		if r.NormalizedRemote == remote {
			max := 3
			if r.AttentionFixAttempts != nil {
				max = *r.AttentionFixAttempts
			}
			return r.AutoRetry == nil || *r.AutoRetry, max
		}
	}
	return false, 3
}
