package agentenv

import (
	"log"
	"os"
	"strings"
)

// Package agentenv decides what an agent subprocess is allowed to see of the
// environment Golem is running in.
//
// It is a package rather than a helper inside one caller because three places
// need the same answer, and a rule that held in some of them would not be a
// rule:
//
//   - the shem worker's `claude --print` (internal/shem/worker);
//   - the CLI-mode backend adapter behind `golem observer dispatch` and
//     `golem ticket review` (internal/agentrunner). CLI mode ingests GitHub
//     issues too — `golem issue sync`, `golem ticket new --from-issue N` — so
//     untrusted description text reaches an agent there as well, with no
//     approval gate in front of it at all;
//   - gate commands (internal/gate). Those are lines of shell out of
//     .golem/config.yaml, a file inside the repository an agent works in and
//     can edit, executed later by whichever process next runs
//     `golem ticket review` on it. Without this they were the one path left
//     by which an agent reached GOLEM_GITHUB_TOKEN — not from its own shell,
//     but from an operator's, one command later.
//
// The property, stated once so it can be checked: nothing Golem executes that
// runs agent-authored or repository-authored instructions holds Golem's own
// credentials. Golem's own commands — `golem ticket new`, `golem ticket
// advance`, `git push` — are a different thing and keep the full environment,
// which is what leaves the shem's credential helper working.
//
// The agent subprocess runs with an allow-listed environment, not Golem's own.
//
// WHY. The ticket description reaches a `claude --print` prompt, and the
// description is untrusted: it is an issue body a stranger can write. The
// approval gate exists to make a successful prompt injection unlikely, not
// impossible, so what the agent process HOLDS when one succeeds is a question
// that has to be answered separately. Before this the answer was "everything
// Golem holds", because nothing in the tree ever set cmd.Env. That included
// GOLEM_GITHUB_TOKEN — a PAT with write access to the watched repository,
// which internal/cli/issue.go reads by exactly that name, so `golem issue
// sync` and `golem ticket new --from-issue N` were ready-made tooling already
// on PATH. An injection that used to get a shell got a repo-write credential
// as well.
//
// This does not close prompt injection and is not meant to. It bounds the
// blast radius of one: the agent keeps what it needs to do the job and loses
// the credentials that are Golem's business rather than the job's.
//
// WHAT IS DENIED, ABSOLUTELY. Everything Golem itself reads. Every such
// variable in the tree lives in the GOLEM_ namespace bar one (ORCHESTRATOR_DB),
// and TestAgentEnvStripsEveryVariableGolemItselfReads walks the source to keep
// that true rather than trusting this comment. The deny is checked first and
// the operator escape hatch below cannot override it.
//
// WHAT IS ALLOWED. The agent's own model credentials; the shell, locale, proxy
// and TLS settings any subprocess needs; git identity; and the language
// toolchains the shipped shem image carries, because the agent's whole job is
// to build and test a repository. An allow-list can always miss one, which is
// what GOLEM_AGENT_ENV is for.
const (
	// golemNamespace prefixes every environment variable that is Golem's own
	// configuration. Nothing matching it is ever passed to an agent.
	golemNamespace = "GOLEM_"
	// passthroughVar names a comma-separated list of additional
	// variable names to pass through, for a toolchain the allow-list below
	// does not anticipate. It cannot widen the deny above.
	passthroughVar = "GOLEM_AGENT_ENV"
)

// golemOwnedNames are the variables Golem reads that do not carry the
// GOLEM_ prefix. Keep in step with TestAgentEnvStripsEveryVariableGolemItselfReads,
// which will fail if a new one appears.
var golemOwnedNames = map[string]bool{
	"ORCHESTRATOR_DB": true,
}

// envNames are passed through by exact match.
var envNames = map[string]bool{
	// Process basics.
	"PATH": true, "HOME": true, "USER": true, "LOGNAME": true, "SHELL": true,
	"PWD": true, "TMPDIR": true, "TMP": true, "TEMP": true, "TERM": true,
	"TZ": true, "LANG": true, "LANGUAGE": true, "CI": true,
	// Egress and trust roots, without which every network call fails behind
	// a corporate proxy or a private CA.
	"HTTP_PROXY": true, "HTTPS_PROXY": true, "ALL_PROXY": true, "NO_PROXY": true,
	"http_proxy": true, "https_proxy": true, "all_proxy": true, "no_proxy": true,
	"SSL_CERT_FILE": true, "SSL_CERT_DIR": true, "CURL_CA_BUNDLE": true,
	"REQUESTS_CA_BUNDLE": true,
	// Native build settings.
	"LD_LIBRARY_PATH": true, "DYLD_LIBRARY_PATH": true, "PKG_CONFIG_PATH": true,
	"CC": true, "CXX": true, "CFLAGS": true, "CXXFLAGS": true, "LDFLAGS": true,
	"MAKEFLAGS": true,
	// Go, which the shem image ships.
	"GOROOT": true, "GOPATH": true, "GOBIN": true, "GOCACHE": true, "GOMODCACHE": true,
	"GOFLAGS": true, "GOPROXY": true, "GOPRIVATE": true, "GOSUMDB": true,
	"GONOSUMDB": true, "GOTOOLCHAIN": true, "GOOS": true, "GOARCH": true,
	"CGO_ENABLED": true,
	// Python, which the shem image ships.
	"VIRTUAL_ENV": true, "PYTHONPATH": true, "PYTHONHOME": true,
	// JVM and Android, for repositories that need them.
	"JAVA_HOME": true, "ANDROID_HOME": true, "ANDROID_SDK_ROOT": true,
}

// envPrefixes are passed through by prefix match.
var envPrefixes = []string{
	"LC_", "XDG_",
	// The agent's own model credentials and configuration. AWS_ and GOOGLE_
	// are here because Claude Code reaches Bedrock and Vertex through them;
	// on a host that uses neither, they are worth unsetting.
	"ANTHROPIC_", "CLAUDE_", "AWS_", "GOOGLE_", "CLOUD_ML_", "VERTEX_",
	// Git identity and behaviour. Note that the shem's push credential is
	// NOT here: deploy/shem-entrypoint.sh installs a helper that expands
	// GOLEM_GITHUB_TOKEN at use time, and that name is denied above, so the
	// helper works for Golem's own `git push` and yields nothing for the
	// agent's.
	"GIT_",
	// Language toolchains.
	"NODE_", "NPM_", "npm_config_", "YARN_", "PNPM_",
	"PIP_", "POETRY_", "CONDA_", "PYENV_", "RBENV_", "NVM_", "SDKMAN_",
	"CARGO_", "RUSTUP_", "RUST_", "MAVEN_", "GRADLE_", "DOTNET_", "NUGET_",
	"GEM_", "BUNDLE_",
}

// Filter reduces parent (an os.Environ()-shaped slice) to what an agent
// subprocess is allowed to see.
func Filter(parent []string) []string {
	extra := passthroughNames(parent)
	out := make([]string, 0, len(parent))
	for _, kv := range parent {
		name, _, ok := strings.Cut(kv, "=")
		if !ok || name == "" {
			// os.Environ() does not produce these, but a caller can, and
			// guessing at what a separator-less entry meant is worse than
			// dropping it.
			continue
		}
		if denied(name) {
			continue
		}
		if envNames[name] || extra[name] || hasAllowedPrefix(name) {
			out = append(out, kv)
		}
	}
	return out
}

// denied reports whether name is Golem's own configuration. Nothing overrides
// it.
func denied(name string) bool {
	return strings.HasPrefix(name, golemNamespace) || golemOwnedNames[name]
}

func hasAllowedPrefix(name string) bool {
	for _, p := range envPrefixes {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return false
}

// passthroughNames reads the operator's GOLEM_AGENT_ENV list out of parent.
// A name in Golem's own namespace is refused and said so out loud, because an
// operator who wrote it there is expecting it to arrive.
func passthroughNames(parent []string) map[string]bool {
	names := map[string]bool{}
	for _, kv := range parent {
		name, value, ok := strings.Cut(kv, "=")
		if !ok || name != passthroughVar {
			continue
		}
		for _, n := range strings.Split(value, ",") {
			n = strings.TrimSpace(n)
			if n == "" {
				continue
			}
			if denied(n) {
				log.Printf("shem: %s lists %q, which is golem's own configuration and is never passed to an agent; ignoring it",
					passthroughVar, n)
				continue
			}
			names[n] = true
		}
	}
	return names
}

// Environ is Filter over this process's own environment. It is what every
// agent exec site passes to cmd.Env.
func Environ() []string { return Filter(os.Environ()) }
