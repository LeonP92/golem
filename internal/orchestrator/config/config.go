package config

import (
	"log"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

// TLSConfig holds TLS certificate and key paths.
type TLSConfig struct {
	Cert string `yaml:"cert"`
	Key  string `yaml:"key"`
}

// CSPConfig controls the orchestrator's Content-Security-Policy header. Mode
// is one of "enforce" (default), "report-only", or "off"; see Load for
// validation and fallback behavior.
type CSPConfig struct {
	Mode string `yaml:"mode"`
}

// GitHubConfig holds settings for the GitHub Issues integration. Durations are
// strings parsed by time.ParseDuration; an empty or unparseable value falls
// back to the documented default rather than failing startup.
type GitHubConfig struct {
	TokenEnv           string `yaml:"token_env"`
	PollInterval       string `yaml:"poll_interval"`
	DrainInterval      string `yaml:"drain_interval"`
	ManualSyncCooldown string `yaml:"manual_sync_cooldown"`
	APIBase            string `yaml:"api_base"`
}

const (
	defaultPollInterval       = 15 * time.Minute
	defaultDrainInterval      = 20 * time.Second
	defaultManualSyncCooldown = time.Minute
	defaultTokenEnv           = "GOLEM_GITHUB_TOKEN"
	defaultCSPMode            = "enforce"
)

// PollIntervalDuration returns the ingest interval, defaulting to 15 minutes.
func (g GitHubConfig) PollIntervalDuration() time.Duration {
	return parseDurationOr("github.poll_interval", g.PollInterval, defaultPollInterval)
}

// DrainIntervalDuration returns the outbox drain interval, defaulting to 20s.
// It is deliberately independent of the ingest interval: ingest polls a mostly
// idle external system, while the drain reacts to local events and must stay
// prompt for retry backoff to mean anything.
func (g GitHubConfig) DrainIntervalDuration() time.Duration {
	return parseDurationOr("github.drain_interval", g.DrainInterval, defaultDrainInterval)
}

// ManualSyncCooldownDuration returns the minimum gap between manual syncs of
// one repo, defaulting to 1 minute.
func (g GitHubConfig) ManualSyncCooldownDuration() time.Duration {
	return parseDurationOr("github.manual_sync_cooldown", g.ManualSyncCooldown, defaultManualSyncCooldown)
}

// parseDurationOr parses raw as a duration, falling back to fallback when raw
// is empty, unparseable, or non-positive. An empty raw is the documented way
// to ask for the default and is silent; an actually-invalid value (e.g. a
// typo like "15mn") is not silently discarded — it is logged, naming the
// config key, the bad value, and the fallback applied, so a startup typo is
// visible in the logs instead of just quietly behaving like it was never set.
func parseDurationOr(name, raw string, fallback time.Duration) time.Duration {
	if raw == "" {
		return fallback
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		log.Printf("config: %s: invalid duration %q (%v) — using default %v", name, raw, err, fallback)
		return fallback
	}
	if d <= 0 {
		log.Printf("config: %s: duration %q must be positive — using default %v", name, raw, fallback)
		return fallback
	}
	return d
}

// Config holds all orchestrator server configuration.
type Config struct {
	Port          int          `yaml:"port"`
	DBPath        string       `yaml:"db_path"`
	SessionSecret string       `yaml:"session_secret"`
	BaseURL       string       `yaml:"base_url"`
	TLS           TLSConfig    `yaml:"tls"`
	GitHub        GitHubConfig `yaml:"github"`
	CSP           CSPConfig    `yaml:"csp"`
}

// Load reads and parses the YAML config file at path.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}
	if cfg.GitHub.TokenEnv == "" {
		cfg.GitHub.TokenEnv = defaultTokenEnv
	}
	switch cfg.CSP.Mode {
	case "":
		cfg.CSP.Mode = defaultCSPMode
	case "enforce", "report-only", "off":
		// valid as configured.
	default:
		log.Printf("config: csp.mode: invalid value %q (must be enforce, report-only, or off) — using default %q",
			cfg.CSP.Mode, defaultCSPMode)
		cfg.CSP.Mode = defaultCSPMode
	}
	return &cfg, nil
}
