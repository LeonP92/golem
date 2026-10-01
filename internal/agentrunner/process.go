package agentrunner

import (
	"context"
	"errors"
	"os/exec"
	"time"

	"github.com/leonp92/golem/internal/agentenv"
)

// agentCmd builds an agent process with golem's secrets stripped and
// privileges dropped.
func agentCmd(ctx context.Context, o Options, dir string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, args[0], args[1:]...) //nolint:gosec // argv is adapter-built; the model id is validated
	cmd.Dir = dir
	// The prompt carries untrusted text, so the agent never gets golem's own
	// environment.
	cmd.Env = append(agentenv.Environ(), agentenv.FilterPairs(o.Env)...)
	// Unprivileged where configured: as root the agent could read golem's
	// secrets from /proc/1/environ.
	agentenv.DropPrivileges(cmd)
	// Bounds the wait for output pipes once the process ends, in case a child
	// it spawned still holds them open.
	cmd.WaitDelay = 5 * time.Second
	return cmd
}

// runAgentCmd runs cmd. A process that exited 0 but left a child holding its
// output open still succeeded.
func runAgentCmd(cmd *exec.Cmd) error {
	err := cmd.Run()
	if errors.Is(err, exec.ErrWaitDelay) && cmd.ProcessState != nil && cmd.ProcessState.Success() {
		return nil
	}
	return err
}
