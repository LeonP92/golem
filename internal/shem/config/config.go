package config

import (
	"os"

	"github.com/leonp92/golem/internal/orchestrator/urlnorm"
	"gopkg.in/yaml.v3"
)

// RepoConfig holds configuration for a single git repository managed by this shem.
type RepoConfig struct {
	Path             string `yaml:"path"`
	Remote           string `yaml:"remote"`
	NormalizedRemote string `yaml:"-"`
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
		cfg.Repos[i].NormalizedRemote = urlnorm.Normalize(cfg.Repos[i].Remote)
	}
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
