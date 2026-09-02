# internal/config

The config module is responsible for loading and validating Golem's configuration from a YAML file. It defines the top-level Config struct and its nested types (GateConfig, PolicyConfig, GraphConfig), exposes a Load function that reads and parses the file while enforcing that the backend field is present, and provides a helper for resolving per-backend ask-and-wait timeout durations. It exists as the single authoritative source of runtime configuration for the rest of the system.

## Functions

- func Load(path string) (*Config, error) — reads and validates a config.yaml, requiring a non-empty backend field
- func (c *Config) AskAndWaitTimeoutFor(backend string) (time.Duration, error) — returns the configured ask-and-wait timeout for the given backend, or an error if not set

## Types

- Config — top-level configuration struct holding backend, gate, tool policy, timeouts, role models, and graph settings
- GateConfig — holds the list of gate commands to run before accepting agent work
- PolicyConfig — defines tool policy rules: allowed network hosts and worktree-only restriction
- GraphConfig — configuration for the graph subsystem: ignore patterns, max file size, and extra extensions
