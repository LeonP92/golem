// Package tester holds the deterministic pieces of the independent tester:
// the verdict contract and the sandbox the tester stage runs acceptance
// commands in (the tester agent itself runs through the normal adapter).
//
// The rules:
//
//   - No secrets. Tester scenarios execute generated code. The environment
//     starts from the agent allow-list (internal/agentenv), which already
//     strips everything Golem-owned, and then removes what an agent needs
//     but a test run does not: the model credentials and SDK configuration
//     an agent CLI reads, and any GITHUB_/GH_ token the repository's own
//     tooling might pick up.
//   - No network by default. The proxy and CA variables that make egress
//     work are stripped, so a scenario that tries to reach out fails rather
//     than succeeding quietly. Command wraps the run in a network
//     namespace when the platform offers one and reports whether it did, so
//     a scenario that needs network can be marked inconclusive instead of
//     being mistaken for a pass on a host that could not isolate it.
package tester

import (
	"context"
	"os/exec"
	"strings"
	"sync"

	"github.com/leonp92/golem/internal/agentenv"
)

// deniedEnvExact are allow-listed variables a tester run must not keep:
// the egress settings that make "no network by default" real.
var deniedEnvExact = map[string]bool{
	"HTTP_PROXY": true, "HTTPS_PROXY": true, "ALL_PROXY": true, "NO_PROXY": true,
	"http_proxy": true, "https_proxy": true, "all_proxy": true, "no_proxy": true,
	"SSL_CERT_FILE": true, "SSL_CERT_DIR": true, "CURL_CA_BUNDLE": true,
	"REQUESTS_CA_BUNDLE": true,
}

// deniedEnvPrefixes are the credential and vendor-SDK prefixes the agent
// allow-list passes through for the agent's own model access. A scenario
// run is not an agent call and holds none of them.
var deniedEnvPrefixes = []string{
	"ANTHROPIC_", "CLAUDE_", "AWS_", "GOOGLE_", "CLOUD_ML_", "VERTEX_",
	"GITHUB_", "GH_",
}

// Env returns the environment for one tester scenario run.
func Env() []string {
	var out []string
	for _, kv := range agentenv.Environ() {
		name, _, _ := strings.Cut(kv, "=")
		if deniedEnvExact[name] {
			continue
		}
		denied := false
		for _, p := range deniedEnvPrefixes {
			if strings.HasPrefix(name, p) {
				denied = true
				break
			}
		}
		if !denied {
			out = append(out, kv)
		}
	}
	return out
}

// Command builds a scenario command in dir with the sandbox environment.
// When the host can create a network namespace the command runs inside
// one, making "no network" a property of the process rather than of the
// environment; networkIsolated reports which happened. A false result is
// information, not a failure: the caller records it so a scenario whose
// failure mode depends on egress is read as inconclusive, never as a pass.
func Command(ctx context.Context, dir, name string, args ...string) (cmd *exec.Cmd, networkIsolated bool) {
	argv := append([]string{name}, args...)
	if unshare, ok := netnsUnshare(); ok {
		argv = append([]string{unshare, "--net", "--"}, argv...)
		networkIsolated = true
	}
	cmd = exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir = dir
	cmd.Env = Env()
	agentenv.DropPrivileges(cmd) // never as root
	return cmd, networkIsolated
}

// netnsUnshare reports the unshare(1) path when this host can actually
// create a network namespace. Existence of the binary is not enough:
// unprivileged user namespaces are disabled on some hosts, and wrapping
// every scenario in a wrapper that cannot run would fail them all for a
// reason that has nothing to do with the code under test. Probed once.
var (
	netnsOnce   sync.Once
	netnsPath   string
	netnsUsable bool
)

func netnsUnshare() (string, bool) {
	netnsOnce.Do(func() {
		path, err := exec.LookPath("unshare")
		if err != nil {
			return
		}
		probe := exec.Command(path, "--net", "--", "true")
		probe.Env = Env()
		agentenv.DropPrivileges(probe) // probe as the account scenarios run as
		if probe.Run() != nil {
			return
		}
		netnsPath, netnsUsable = path, true
	})
	return netnsPath, netnsUsable
}
