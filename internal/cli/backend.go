package cli

import (
	"flag"
	"fmt"

	"github.com/leonp92/golem/internal/agentrunner"
	"github.com/leonp92/golem/internal/config"
	"github.com/leonp92/golem/internal/models"
)

// RunnerSource is everything that decides which adapter a command builds.
type RunnerSource struct {
	Config        *config.Config
	WorktreeRoot  string
	BackendConfig string
	Backend       string
}

// NewRunner builds the adapter from --backend-config, else --backend, else
// .golem/config.yaml.
func NewRunner(src RunnerSource) (agentrunner.Adapter, error) {
	if src.BackendConfig != "" {
		b, err := agentrunner.LoadBackendConfig(src.BackendConfig)
		if err != nil {
			return nil, err
		}
		if src.Backend != "" && src.Backend != b.Adapter {
			return nil, fmt.Errorf("--backend %s disagrees with --backend-config adapter %s", src.Backend, b.Adapter)
		}
		return b.NewAdapter(src.WorktreeRoot)
	}
	name := src.Backend
	if name == "" {
		name = src.Config.Backend
	}
	return agentrunner.New(name, agentrunner.Options{RepoRoot: src.WorktreeRoot})
}

type modelFlags struct{ model, backendConfig, backend *string }

func addModelFlags(fs *flag.FlagSet) modelFlags {
	return modelFlags{
		model:         fs.String("model", "", "model id for this invocation"),
		backendConfig: fs.String("backend-config", "", "path to a backend config document"),
		backend:       fs.String("backend", "", "registered adapter name"),
	}
}

func (m modelFlags) runner(cfg *config.Config, worktreeRoot string) (agentrunner.Adapter, error) {
	return NewRunner(RunnerSource{
		Config:        cfg,
		WorktreeRoot:  worktreeRoot,
		BackendConfig: *m.backendConfig,
		Backend:       *m.backend,
	})
}

// resolve returns the --model value, else role_models[role], else
// role_models[stage], else "". role_models is ignored with --backend-config
// because .golem/config.yaml is agent-writable.
func (m modelFlags) resolve(cfg *config.Config, role string, stage models.Stage) (string, error) {
	id := *m.model
	switch {
	case id != "":
	case *m.backendConfig != "":
		return "", nil
	case cfg.RoleModels[role] != "":
		id = cfg.RoleModels[role]
	default:
		id = cfg.RoleModels[string(stage)]
	}
	if id != "" && !models.ValidModelID(id) {
		return "", fmt.Errorf("invalid model id %q", id)
	}
	return id, nil
}

// validate reports a bad --model, --backend or --backend-config.
func (m modelFlags) validate(cfg *config.Config) error {
	if _, err := m.resolve(cfg, "", ""); err != nil {
		return err
	}
	if *m.backendConfig != "" || *m.backend != "" {
		if _, err := m.runner(cfg, ""); err != nil {
			return fmt.Errorf("selecting backend: %w", err)
		}
	}
	return nil
}
