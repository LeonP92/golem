package config

import (
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
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
	DefaultLabel       string `yaml:"default_label"`
	PollInterval       string `yaml:"poll_interval"`
	DrainInterval      string `yaml:"drain_interval"`
	ManualSyncCooldown string `yaml:"manual_sync_cooldown"`
	APIBase            string `yaml:"api_base"`
	PRFixAttempts      int    `yaml:"pr_fix_attempts"`
	PRMonitorInterval  string `yaml:"pr_monitor_interval"`
}

const (
	defaultPollInterval       = 15 * time.Minute
	defaultDrainInterval      = 20 * time.Second
	defaultManualSyncCooldown = time.Minute
	defaultPRMonitorInterval  = 2 * time.Minute
	defaultTokenEnv           = "GOLEM_GITHUB_TOKEN"
	defaultPRFixAttemptsEnv   = "GOLEM_PR_FIX_ATTEMPTS"
	defaultPRFixAttempts      = 3
	defaultLabelEnv           = "GOLEM_GITHUB_LABEL"
	// defaultTriggerLabel mirrors ghsync.DefaultTriggerLabel. Duplicated
	// rather than imported to keep config free of a dependency on the sync
	// engine, the same way api.defaultGitHubTokenEnv mirrors defaultTokenEnv
	// here. ghsync's TestDefaultTriggerLabelMatchesConfig pins them equal.
	defaultTriggerLabel = "golem"
	defaultCSPMode      = "enforce"
)

// TriggerLabel returns the label a newly registered repository starts with,
// resolved in order: the GOLEM_GITHUB_LABEL environment variable, then
// github.default_label in the config file, then "golem".
//
// The environment wins because that is where a Docker deployment sets it —
// orchestrator.yaml is bind-mounted read-only and .env is the file an
// operator already edits. It is not a secret, so it is read directly rather
// than through a token_env-style indirection.
//
// This is a DEFAULT, not an override: it seeds the Label column when a
// repository is first registered and is what the settings page offers for one
// that is not registered yet. A repository whose label was already saved keeps
// it, because that value is a deliberate per-repo choice and silently
// rewriting it from the environment would discard it.
func (g GitHubConfig) TriggerLabel() string {
	if env := strings.TrimSpace(os.Getenv(defaultLabelEnv)); env != "" {
		return env
	}
	if g.DefaultLabel != "" {
		return g.DefaultLabel
	}
	return defaultTriggerLabel
}

// UnlimitedPRFixAttempts is what MaxPRFixAttempts returns for
// GOLEM_PR_FIX_ATTEMPTS=-1: keep trying for as long as the pull request is
// open. Negative so no real cap can be mistaken for it, and so callers test
// with `max >= 0` rather than against a magic number.
const UnlimitedPRFixAttempts = -1

// MaxPRFixAttempts is how many times the shem may try to fix a pull request
// — a failing check or a merge conflict — before the ticket stops and waits
// for a human. Resolved from GOLEM_PR_FIX_ATTEMPTS, then
// github.pr_fix_attempts, then 3.
//
// A cap is not optional. Each attempt is a full agent run, a push, and a CI
// cycle, and a failure the agent cannot fix produces exactly the same
// failure again: without a bound that is an unending loop that spends money
// and fills the pull request with commits for as long as nobody notices.
//
// Zero is a real setting, not an unset one: it means never attempt a fix,
// report the problem and wait. That is why this does not use the "0 means
// use the default" shorthand that ListenPort can afford — there, 0 is not a
// port anyone can mean.
//
// -1 is unlimited. It is safe against the obvious runaway because the
// monitor will not dispatch twice for the same head commit: an agent that
// changes nothing cannot re-trigger itself. What it does NOT bound is an
// agent that keeps producing different, useless commits, so it is opt-in.
//
// Every OTHER negative falls back rather than being treated as unlimited.
// A typo in the one setting whose job is to bound cost should not be the
// thing that removes the bound.
func (g GitHubConfig) MaxPRFixAttempts() int {
	if raw := strings.TrimSpace(os.Getenv(defaultPRFixAttemptsEnv)); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && (n >= 0 || n == UnlimitedPRFixAttempts) {
			return n
		}
	}
	if g.PRFixAttempts > 0 || g.PRFixAttempts == UnlimitedPRFixAttempts {
		return g.PRFixAttempts
	}
	return defaultPRFixAttempts
}

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

// PRMonitorIntervalDuration returns how often open pull requests are checked
// for failing checks, conflicts and closure. Defaults to 2 minutes.
//
// Deliberately its own interval rather than sharing the drain ticker. Drain
// runs every 20 seconds because it is delivering queued writes and latency
// there is user-visible; the monitor spends two GitHub API calls PER OPEN
// PULL REQUEST per pass, so at the drain rate a handful of open pull
// requests would burn the hourly rate limit on polling alone. It is also
// slower than ingest's 15 minutes, because a pull request sitting red with
// nobody told is worse than an issue being noticed late.
func (g GitHubConfig) PRMonitorIntervalDuration() time.Duration {
	return parseDurationOr("github.pr_monitor_interval", g.PRMonitorInterval, defaultPRMonitorInterval)
}

// ManualSyncCooldownDuration returns the minimum gap between manual syncs of
// one repo, defaulting to 1 minute.
func (g GitHubConfig) ManualSyncCooldownDuration() time.Duration {
	return parseDurationOr("github.manual_sync_cooldown", g.ManualSyncCooldown, defaultManualSyncCooldown)
}

// parseDurationOr parses raw as a duration, falling back to fallback when raw
// is empty, unparseable, or non-positive.
//
// It does NOT log. Load reports every bad value once, eagerly — see
// reportBadDurations — so these accessors are safe to call from anywhere and
// as often as a caller likes. They used to log themselves, which had two
// consequences: a typo was announced once per call site when GitHub sync
// started, and announced not at all when it did not, so
// "poll_interval: 15mn" on an install with no token or no enabled
// repositories produced a completely silent startup.
func parseDurationOr(name, raw string, fallback time.Duration) time.Duration {
	if raw == "" {
		return fallback
	}
	d, problem := parseDuration(name, raw, fallback)
	if problem != "" {
		return fallback
	}
	return d
}

// parseDuration returns the parsed duration and, when the value is unusable,
// the operator-facing complaint about it. An empty raw is the documented way
// to ask for the default and is never a complaint.
func parseDuration(name, raw string, fallback time.Duration) (time.Duration, string) {
	if raw == "" {
		return fallback, ""
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		return fallback, fmt.Sprintf("config: %s: invalid duration %q (%v) — using default %v",
			name, raw, err, fallback)
	}
	if d <= 0 {
		return fallback, fmt.Sprintf("config: %s: duration %q must be positive — using default %v",
			name, raw, fallback)
	}
	return d, ""
}

// reportBadDurations logs one line per unusable duration in the github block.
//
// Called from Load, unconditionally, for the same reason csp.mode is
// validated there: a configuration typo is a startup fact, not a fact about
// whichever subsystem happens to read the value later. Nothing here changes a
// value — the accessors apply the same fallbacks whether or not this ran.
func reportBadDurations(g GitHubConfig) {
	for _, d := range []struct {
		name     string
		raw      string
		fallback time.Duration
	}{
		{"github.poll_interval", g.PollInterval, defaultPollInterval},
		{"github.drain_interval", g.DrainInterval, defaultDrainInterval},
		{"github.manual_sync_cooldown", g.ManualSyncCooldown, defaultManualSyncCooldown},
	} {
		if _, problem := parseDuration(d.name, d.raw, d.fallback); problem != "" {
			log.Print(problem)
		}
	}
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

// defaultPortEnv is read by ListenPort. Not a secret, so it is read
// directly rather than through a token_env-style indirection, exactly as
// defaultLabelEnv is.
const defaultPortEnv = "GOLEM_PORT"

// defaultPort is the port the orchestrator listens on when nothing says
// otherwise.
const defaultPort = 8080

// ListenPort returns the port to serve on, resolved in order: the GOLEM_PORT
// environment variable, then `port` in the config file, then 8080.
//
// The environment wins for the reason it wins in TriggerLabel: in the Docker
// stack orchestrator.yaml is bind-mounted read-only, so .env is the only file
// an operator can actually edit. The usual reason to move it is that
// something else on the host already holds 8080.
//
// A value that cannot be listened on is ignored rather than passed through.
// Port 0 is the trap worth naming: net/http treats ":0" as "any free port",
// so an empty or unparseable setting would start the server cleanly on a
// random port that nothing else in the stack knows about. Falling back is
// louder than that, because the operator finds the orchestrator where the
// documentation says it is.
func (c Config) ListenPort() int {
	if p, ok := validPort(strings.TrimSpace(os.Getenv(defaultPortEnv))); ok {
		return p
	}
	if c.Port > 0 && c.Port <= 65535 {
		return c.Port
	}
	return defaultPort
}

// validPort parses a port from its decimal string form, reporting whether it
// is one a server can actually bind.
func validPort(raw string) (int, bool) {
	if raw == "" {
		return 0, false
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 || n > 65535 {
		return 0, false
	}
	return n, true
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
	reportBadDurations(cfg.GitHub)
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
