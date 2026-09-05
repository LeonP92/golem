package cli

import (
	"fmt"

	"github.com/leonp92/golem/internal/agentrunner"
	"github.com/leonp92/golem/internal/config"
)

func NewRunner(cfg *config.Config, worktreeRoot string) (agentrunner.Runner, error) {
	switch cfg.Backend {
	case "claude-code":
		return agentrunner.ClaudeCode{RepoRoot: worktreeRoot}, nil
	default:
		return nil, fmt.Errorf("unsupported backend %q", cfg.Backend)
	}
}
