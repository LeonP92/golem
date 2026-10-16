package gate

import (
	"os/exec"
	"strings"

	"github.com/leonp92/golem/internal/agentenv"
	"github.com/leonp92/golem/internal/config"
)

type Result struct {
	Passed bool
	Output string
	// CommandsRun is how many configured gate commands actually executed.
	// A gate with no commands passes vacuously; the evidence entry shows 0.
	CommandsRun int
	// HeadSHA is the commit the gate ran against, so evidence can be bound
	// to an exact candidate and goes stale when the head moves. Empty when
	// the directory is not a git worktree.
	HeadSHA string
}

// Run stops at the first failing command rather than running the rest.
//
// A gate command is not Golem's command. It is a line of shell out of
// .golem/config.yaml — a file inside the repository an agent works in, which
// an agent can edit — and it is executed later, by whichever process next runs
// `golem ticket review` on that repository. So it runs with the agent's
// allow-listed environment rather than the ambient one (see
// internal/agentenv): otherwise an agent could write a gate command today and
// have it read GOLEM_GITHUB_TOKEN tomorrow, out of an operator's own shell on
// a co-located box. That was the last path making "the agent never sees
// GOLEM_*" untrue.
//
// The allow-list is the right one here for the reason that justifies it for
// the agent: gate commands are build and test commands, run in the same
// repository and against the same toolchain the agent uses, so whatever
// suffices for the agent's own `make test` suffices for the gate's. Nothing
// here pushes — internal/cli contains no push at all; the branch push lives in
// internal/shem/worker.pushTicketBranch, which is one of Golem's own commands
// and keeps the full environment. GOLEM_AGENT_ENV remains the escape hatch for
// a gate that genuinely needs more, and it cannot re-add the GOLEM_ namespace.
func Run(dir string, cfg config.GateConfig) (Result, error) {
	var output strings.Builder
	env := agentenv.Environ()
	res := Result{Passed: true, HeadSHA: headSHA(dir)}
	for _, command := range cfg.Commands {
		cmd := exec.Command("sh", "-c", command)
		cmd.Dir = dir
		cmd.Env = env
		out, err := cmd.CombinedOutput()
		output.Write(out)
		res.CommandsRun++
		if err != nil {
			res.Passed = false
			res.Output = output.String()
			return res, nil
		}
	}
	res.Output = output.String()
	return res, nil
}

// headSHA binds the gate result to the exact commit it ran against. It is
// best-effort: a directory that is not a git worktree yields an empty SHA,
// and callers treat empty as unverifiable, not as current.
func headSHA(dir string) string {
	cmd := exec.Command("git", "rev-parse", "HEAD")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
