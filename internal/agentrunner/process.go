package agentrunner

import (
	"context"
	"os/exec"
	"time"

	"github.com/leonp92/golem/internal/agentenv"
)

// agentCmd builds an agent process with golem's secrets stripped and
// privileges dropped.
func agentCmd(ctx context.Context, o Options, dir string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, args[0], args[1:]...) //nolint:gosec // argv is adapter-built; the model id is validated
	cmd.Dir = dir
	// The prompt carries untrusted text — BuildPrompt wraps log entries and a
	// diff, and on a repository synced from GitHub the log's first line is the
	// issue description — so this process does not get golem's own
	// environment. See internal/agentenv for why the allow-list is shared
	// rather than duplicated.
	cmd.Env = append(agentenv.Environ(), agentenv.FilterPairs(o.Env)...)
	// And as an unprivileged user where one is configured. Filtering the
	// environment is only meaningful if the agent cannot read the shem's
	// memory: as root in the same container it reads /proc/1/environ instead.
	agentenv.DropPrivileges(cmd)
	// Bounds the wait for output pipes after ctx kills the process, in case a
	// child it spawned still holds them open.
	cmd.WaitDelay = 5 * time.Second
	return cmd
}
