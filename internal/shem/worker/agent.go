package worker

import (
	"context"
	"io"
	"log"
	"os"
	"path/filepath"

	"github.com/leonp92/golem/internal/agentrunner"
	"github.com/leonp92/golem/internal/models"
	"github.com/leonp92/golem/internal/shem/client"
	"github.com/leonp92/golem/internal/shem/config"
)

// Agent is the adapter this shem runs plus the config document its nested
// golem subcommands read.
type Agent struct {
	Adapter    agentrunner.Adapter
	ConfigPath string
	Catalog    models.Catalog
}

// NewAgent builds the configured adapter and writes its backend document.
// The returned func removes the run directory.
func NewAgent(cfg *config.Config) (*Agent, func(), error) {
	backend := agentrunner.ResolveBackend(cfg.Backend)
	a, err := backend.NewAdapter("")
	if err != nil {
		return nil, nil, err
	}
	dir, err := os.MkdirTemp("", "golem-shem-")
	if err != nil {
		return nil, nil, err
	}
	cleanup := func() { _ = os.RemoveAll(dir) }
	// The agent account runs the nested subcommands and has to read this.
	if err := os.Chmod(dir, 0o755); err != nil {
		cleanup()
		return nil, nil, err
	}
	path := filepath.Join(dir, "backend.yaml")
	if err := backend.Write(path); err != nil {
		cleanup()
		return nil, nil, err
	}
	return &Agent{Adapter: a, ConfigPath: path, Catalog: backend.Catalog}, cleanup, nil
}

// StageModel resolves one stage against this shem's own catalog.
func (a *Agent) StageModel(st models.Stage) string {
	resolved, _ := models.Resolve(a.Catalog, nil)
	return resolved[st]
}

// claimModel returns the model the claim resolved for st, or this shem's own
// default when the claim carries no models (an older orchestrator).
func (e *GolemExecutor) claimModel(c *client.Client, claim *client.ClaimResponse, st models.Stage) string {
	if claim.Models == nil {
		return sanitizeModel(c, claim.TicketID, e.Agent.StageModel(st))
	}
	return sanitizeModel(c, claim.TicketID, claim.Models[string(st)])
}

// subcommandArgs appends the backend config and model flags.
func (a *Agent) subcommandArgs(args []string, model string) []string {
	args = append(args, "--backend-config", a.ConfigPath)
	if model != "" {
		args = append(args, "--model", model)
	}
	return args
}

// runPhase runs one agent phase, teeing its output to the phase log.
func (e *GolemExecutor) runPhase(ctx context.Context, repoPath, prompt, ticketDir, phase, model string) error {
	logPath := filepath.Join(ticketDir, "agent-"+phase+".log")
	f, err := os.Create(logPath) //nolint:gosec
	if err != nil {
		log.Printf("executor: could not create phase log %s: %v", logPath, err)
		return e.Agent.Adapter.RunPhase(ctx, repoPath, prompt, os.Stdout, model)
	}
	defer func() { _ = f.Close() }()
	return e.Agent.Adapter.RunPhase(ctx, repoPath, prompt, io.MultiWriter(os.Stdout, f), model)
}
