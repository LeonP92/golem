package agentrunner

import (
	"context"
	"errors"
	"io"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/leonp92/golem/internal/agentenv"
	"github.com/leonp92/golem/internal/evidence"
)

// agentCmd builds an agent process with golem's secrets stripped and
// privileges dropped.
func agentCmd(ctx context.Context, o Options, dir string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, args[0], args[1:]...) //nolint:gosec // argv is adapter-built; the model id is validated
	cmd.Dir = dir
	// The prompt carries untrusted text, so the agent never gets golem's own
	// environment.
	cmd.Env = append(agentenv.Environ(), agentenv.FilterPairs(o.Env)...)
	// An inherited PWD names golem's cwd, not dir, and opencode trusts it
	// over getcwd. Appended last so it wins.
	if dir != "" {
		if abs, err := filepath.Abs(dir); err == nil {
			cmd.Env = append(cmd.Env, "PWD="+abs)
		}
	}
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

// runCleaned runs cmd with the prompt and every output byte redacted. The
// writer is flushed even when the run fails, so the tail of the output is kept.
func runCleaned(cmd *exec.Cmd, prompt string, out io.Writer) error {
	cmd.Stdin = strings.NewReader(evidence.Clean(prompt))
	safe := evidence.NewWriter(out)
	cmd.Stdout, cmd.Stderr = safe, safe
	runErr := runAgentCmd(cmd)
	if flushErr := safe.Flush(); runErr == nil {
		return flushErr
	}
	return runErr
}
