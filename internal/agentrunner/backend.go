package agentrunner

import (
	_ "embed"
	"fmt"
	"os"
	"strings"

	"github.com/leonp92/golem/internal/models"
	"gopkg.in/yaml.v3"
)

//go:embed claudecode_catalog.yaml
var claudeCodeSeedYAML []byte

// claudeCodeSeed is the embedded claude-code block, parsed once.
var claudeCodeSeed = mustParseSeed()

// mustParseSeed checks Catalog.Validate rather than BackendConfig.Validate:
// the latter builds the adapter through the registry, which claudecode.go's
// init has not necessarily filled at package-variable initialisation.
func mustParseSeed() BackendConfig {
	var b BackendConfig
	if err := yaml.Unmarshal(claudeCodeSeedYAML, &b); err != nil {
		panic("agentrunner: claudecode_catalog.yaml: " + err.Error())
	}
	b.Adapter = "claude-code"
	if err := b.Catalog.Validate(); err != nil {
		panic("agentrunner: claudecode_catalog.yaml: " + err.Error())
	}
	return b
}

// BackendConfig is a shem.yaml `backend:` block.
type BackendConfig struct {
	Adapter        string            `yaml:"adapter"`
	Command        string            `yaml:"command"`
	ExtraArgs      []string          `yaml:"extra_args"`
	Env            map[string]string `yaml:"env"`
	models.Catalog `yaml:",inline"`
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
	return os.WriteFile(path, data, 0o644) //nolint:gosec // read by the agent account
}

// SeedClaudeCode returns the embedded claude-code backend block.
func SeedClaudeCode() BackendConfig { return claudeCodeSeed }

// ResolveBackend fills an absent or catalog-less block from the claude-code
// seed. A block declaring models is returned as written.
func ResolveBackend(b *BackendConfig) BackendConfig {
	if b == nil {
		return claudeCodeSeed
	}
	out := *b
	if out.Adapter == "" {
		out.Adapter = "claude-code"
	}
	if len(out.Models) == 0 && out.Adapter == "claude-code" {
		out.Catalog = claudeCodeSeed.Catalog
	}
	return out
}

// Validate reports the first problem in b: an unknown adapter, an extra_args
// token the adapter sets itself, or a structural fault in the catalog.
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
	return b.Catalog.Validate()
}

// NewAdapter builds b's adapter.
func (b BackendConfig) NewAdapter(repoRoot string) (Adapter, error) {
	return New(b.Adapter, b.Options(repoRoot))
}

func (b BackendConfig) Options(repoRoot string) Options {
	return Options{RepoRoot: repoRoot, Command: b.Command, ExtraArgs: b.ExtraArgs, Env: b.Env}
}
