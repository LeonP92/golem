package agentrunner

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/leonp92/golem/internal/agentenv"
	"github.com/leonp92/golem/internal/models"
	"gopkg.in/yaml.v3"
)

// DefaultAdapter is the adapter a backend block with none named runs.
const DefaultAdapter = "claude-code"

// BackendConfig is a shem.yaml `backend:` block.
type BackendConfig struct {
	Adapter        string            `yaml:"adapter"`
	Command        string            `yaml:"command"`
	ExtraArgs      []string          `yaml:"extra_args"`
	Env            map[string]string `yaml:"env"`
	models.Catalog `yaml:",inline"`

	// set holds the keys the block declared, so an explicit
	// `supports_selection: false` differs from an absent one.
	set map[string]bool
}

var backendKeys = map[string]bool{
	"adapter": true, "command": true, "extra_args": true, "env": true,
	"supports_selection": true, "tiers": true, "models": true, "stage_defaults": true,
}

// UnmarshalYAML decodes the block, rejecting unknown keys so a misspelling
// fails at startup instead of silently taking the default.
func (b *BackendConfig) UnmarshalYAML(n *yaml.Node) error {
	type plain BackendConfig
	if err := n.Decode((*plain)(b)); err != nil {
		return err
	}
	b.set = map[string]bool{}
	for i := 0; i+1 < len(n.Content); i += 2 {
		key := n.Content[i]
		if !backendKeys[key.Value] {
			return fmt.Errorf("line %d: unknown backend key %q (known: %s)", key.Line, key.Value, knownKeys())
		}
		b.set[key.Value] = true
	}
	return nil
}

func knownKeys() string {
	keys := make([]string, 0, len(backendKeys))
	for k := range backendKeys {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return strings.Join(keys, ", ")
}

type backendDoc struct {
	Backend BackendConfig `yaml:"backend"`
}

// LoadBackendConfig reads a {backend: {...}} document and validates it.
func LoadBackendConfig(path string) (BackendConfig, error) {
	data, err := os.ReadFile(path) //nolint:gosec // an operator-supplied config path
	if err != nil {
		return BackendConfig{}, err
	}
	var doc backendDoc
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return BackendConfig{}, fmt.Errorf("%s: %w", path, err)
	}
	b := ResolveBackend(&doc.Backend)
	if err := b.Validate(); err != nil {
		return BackendConfig{}, fmt.Errorf("%s: %w", path, err)
	}
	return b, nil
}

// MarshalDocument renders b as a {backend: {...}} YAML document.
func (b BackendConfig) MarshalDocument() ([]byte, error) {
	return yaml.Marshal(backendDoc{Backend: b})
}

// Write emits b as a {backend: {...}} document at mode 0o644.
func (b BackendConfig) Write(path string) error {
	data, err := b.MarshalDocument()
	if err != nil {
		return err
	}
	if err := os.WriteFile(path, data, 0o644); err != nil { //nolint:gosec // read by the agent account
		return err
	}
	// WriteFile's mode is masked by the umask.
	return os.Chmod(path, 0o644)
}

// ResolveBackend fills b from its adapter's default catalog. A block that
// declares models is used as written; otherwise only the catalog keys it
// declares override the defaults. A nil block is the default adapter's.
func ResolveBackend(b *BackendConfig) BackendConfig {
	var out BackendConfig
	var set map[string]bool
	if b != nil {
		out, set = *b, b.set
	}
	out.set = nil
	if out.Adapter == "" {
		out.Adapter = DefaultAdapter
	}
	if len(out.Models) > 0 {
		return out
	}
	a, err := New(out.Adapter, Options{})
	if err != nil {
		return out // Validate reports the unknown adapter.
	}
	def := a.DefaultCatalog()
	if !set["supports_selection"] {
		out.SupportsSelection = def.SupportsSelection
	}
	if !set["tiers"] {
		out.Tiers = def.Tiers
	}
	if !set["stage_defaults"] {
		out.StageDefaults = def.StageDefaults
	}
	out.Models = def.Models
	return out
}

// Validate reports the first problem in b: an unknown adapter, an extra_args
// token the adapter sets itself, an env name golem owns, or a structural
// fault in the catalog.
func (b BackendConfig) Validate() error {
	a, err := New(b.Adapter, b.Options(""))
	if err != nil {
		return err
	}
	reserved := map[string]bool{}
	for _, t := range a.ReservedArgs() {
		reserved[t] = true
	}
	for _, arg := range b.ExtraArgs {
		tok := arg
		if i := strings.IndexByte(tok, '='); i >= 0 {
			tok = tok[:i]
		}
		if reserved[tok] {
			return fmt.Errorf("extra_args may not contain %q", arg)
		}
	}
	for name := range b.Env {
		if agentenv.IsGolemOwned(name) {
			return fmt.Errorf("env may not set %s: golem's own variables never reach the agent", name)
		}
	}
	return b.Catalog.Validate()
}

// NewAdapter builds b's adapter.
func (b BackendConfig) NewAdapter(repoRoot string) (Adapter, error) {
	return New(b.Adapter, b.Options(repoRoot))
}

func (b BackendConfig) Options(repoRoot string) Options {
	return Options{RepoRoot: repoRoot, Command: b.Command, ExtraArgs: b.ExtraArgs, Env: b.Env}
}
