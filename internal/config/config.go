package config

import (
	"fmt"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

type GateConfig struct {
	Commands []string `yaml:"commands"`
}

type PolicyConfig struct {
	AllowNetwork      []string `yaml:"allow_network"`
	AllowWorktreeOnly bool     `yaml:"allow_worktree_only"`
}

type GraphConfig struct {
	IgnorePatterns  []string `yaml:"ignore_patterns"`
	MaxFileSizeKB   int      `yaml:"max_file_size_kb"`
	ExtraExtensions []string `yaml:"extra_extensions"`
}

// GitHubConfig configures CLI-mode GitHub access.
//
// Write defaults to false. An orchestrator-managed repository must have
// exactly one writer, or the CLI and the orchestrator will post duplicate
// comments and fight over labels. `golem init` sets Write true for standalone
// use; the shem sets it false for repos it manages.
type GitHubConfig struct {
	Repo  string `yaml:"repo"`  // "org/repo" (required; not inferred from the git remote)
	Label string `yaml:"label"` // trigger label, default "golem"
	Write bool   `yaml:"write"`
}

type Config struct {
	Backend           string            `yaml:"backend"`
	Gate              GateConfig        `yaml:"gate"`
	ToolPolicy        PolicyConfig      `yaml:"tool_policy"`
	AskAndWaitTimeout map[string]string `yaml:"ask_and_wait_timeout"`
	RoleModels        map[string]string `yaml:"role_models"`
	Graph             GraphConfig       `yaml:"graph"`
	GitHub            GitHubConfig      `yaml:"github"`
}

// Load reads and validates a Golem config.yaml. Backend is required —
// golem init never writes a config without one (spec: Distribution &
// Ownership — no silent default).
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", path, err)
	}
	if cfg.Backend == "" {
		return nil, fmt.Errorf("%s: backend is required", path)
	}
	if cfg.GitHub.Label == "" {
		cfg.GitHub.Label = "golem"
	}
	return &cfg, nil
}

func (c *Config) AskAndWaitTimeoutFor(backend string) (time.Duration, error) {
	raw, ok := c.AskAndWaitTimeout[backend]
	if !ok {
		return 0, fmt.Errorf("no ask_and_wait_timeout configured for backend %q", backend)
	}
	return time.ParseDuration(raw)
}
