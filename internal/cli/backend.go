package cli

import (
	"github.com/leonp92/golem/internal/agentrunner"
	"github.com/leonp92/golem/internal/config"
)

func NewRunner(cfg *config.Config, worktreeRoot string) (agentrunner.Adapter, error) {
	return agentrunner.New(cfg.Backend, agentrunner.Options{RepoRoot: worktreeRoot})
}
