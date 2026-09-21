package deploy

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// composeFile is the subset of docker-compose.yml this test reasons about.
// Only the service environment blocks matter here; everything else is
// deliberately ignored so an unrelated compose change cannot break this test.
type composeFile struct {
	Services map[string]struct {
		Environment []string `yaml:"environment"`
		EnvFile     any      `yaml:"env_file"`
	} `yaml:"services"`
}

// repoFile reads a file from the repository root (one level above this
// package).
func repoFile(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", name))
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(b)
}

// envValue returns the right-hand side of the first `NAME=...` entry in a
// service's environment block, and whether it was there at all.
func envValue(env []string, name string) (string, bool) {
	for _, entry := range env {
		if k, v, ok := strings.Cut(entry, "="); ok && k == name {
			return v, true
		}
	}
	return "", false
}

// service looks a service up and refuses to reason about one that has grown an
// env_file, which would deliver every secret in .env into the container and
// would satisfy any check here for the wrong reason.
func service(t *testing.T, compose composeFile, name string) []string {
	t.Helper()
	svc, ok := compose.Services[name]
	if !ok {
		t.Fatalf("docker-compose.yml has no %q service", name)
	}
	if svc.EnvFile != nil {
		t.Fatalf("service %q gained an env_file; revisit this test before relying on it", name)
	}
	return svc.Environment
}

func parseCompose(t *testing.T) composeFile {
	t.Helper()
	var compose composeFile
	if err := yaml.Unmarshal([]byte(repoFile(t, "docker-compose.yml")), &compose); err != nil {
		t.Fatalf("parse docker-compose.yml: %v", err)
	}
	return compose
}

// TestComposeDeliversEachGitHubCredentialOnlyWhereItIsUsED pins the two halves
// of the credential contract, which are not the same variable.
//
// This is a regression test for a shipped-but-unreachable feature and, since
// re-review finding F2, for its overcorrection. .env.example documented
// GOLEM_GITHUB_TOKEN and docker-compose.yml never referenced it, so the whole
// GitHub integration was dead for anyone following the README. The fix then
// delivered that same repo-write PAT to BOTH services — including the shem,
// whose shipped configuration (no_push: true) cannot use it, and whose agent
// subprocess used to inherit the entire environment. A prompt injection in an
// approved issue therefore reached a credential rather than only a shell.
//
// So: the orchestrator gets GOLEM_GITHUB_TOKEN, because it always needs it.
// The shem gets its push credential only if the operator sets a SECOND,
// deliberately separate variable, which is the same moment they turn no_push
// off. The container-side name stays GOLEM_GITHUB_TOKEN so
// deploy/shem-entrypoint.sh's credential helper is unchanged.
func TestComposeDeliversEachGitHubCredentialOnlyWhereItIsUsed(t *testing.T) {
	compose := parseCompose(t)

	t.Run("orchestrator", func(t *testing.T) {
		env := service(t, compose, "orchestrator")
		value, ok := envValue(env, "GOLEM_GITHUB_TOKEN")
		if !ok {
			t.Fatalf("the orchestrator does not receive GOLEM_GITHUB_TOKEN, but it reads it via "+
				"config.github.token_env to poll issues and drain the write outbox.\nenvironment: %v", env)
		}
		if !strings.Contains(value, "${GOLEM_GITHUB_TOKEN") {
			t.Fatalf("the orchestrator sets GOLEM_GITHUB_TOKEN to %q; it must interpolate from .env", value)
		}
	})

	t.Run("shem", func(t *testing.T) {
		env := service(t, compose, "shem")
		value, ok := envValue(env, "GOLEM_GITHUB_TOKEN")
		if !ok {
			t.Fatalf("the shem no longer receives GOLEM_GITHUB_TOKEN at all; "+
				"deploy/shem-entrypoint.sh reads exactly that name to install the push "+
				"credential, so no_push: false can no longer open a pull request.\nenvironment: %v", env)
		}
		if strings.Contains(value, "${GOLEM_GITHUB_TOKEN") {
			t.Fatalf("the shem is handed the orchestrator's GOLEM_GITHUB_TOKEN (%q). "+
				"That ships a repo-write PAT into the container that runs the agent, on a "+
				"default configuration (no_push: true) that cannot use it. It must come from "+
				"a separate opt-in variable.", value)
		}
		if !strings.Contains(value, "${GOLEM_SHEM_GITHUB_TOKEN") {
			t.Fatalf("the shem's GOLEM_GITHUB_TOKEN is %q; it must interpolate from "+
				"GOLEM_SHEM_GITHUB_TOKEN, the opt-in push credential", value)
		}
		if !strings.Contains(value, ":-}") {
			t.Fatalf("the shem's GOLEM_GITHUB_TOKEN is %q; it must default to empty so an "+
				"operator who has not opted in ships no credential", value)
		}
	})
}

// TestEnvExampleDocumentsBothGitHubTokens keeps the variables the compose file
// interpolates and the variables .env.example tells operators to fill in from
// drifting apart — the pair is the whole contract.
func TestEnvExampleDocumentsBothGitHubTokens(t *testing.T) {
	env := repoFile(t, ".env.example")
	for _, name := range []string{"GOLEM_GITHUB_TOKEN", "GOLEM_SHEM_GITHUB_TOKEN"} {
		t.Run(name, func(t *testing.T) {
			for _, line := range strings.Split(env, "\n") {
				line = strings.TrimSpace(line)
				line = strings.TrimPrefix(line, "# ")
				if strings.HasPrefix(line, name+"=") {
					return
				}
			}
			t.Fatalf(".env.example no longer declares %s, but docker-compose.yml interpolates it", name)
		})
	}
}

// shemRepoPaths is the subset needed to check that every repository the shem
// is configured to work in has somewhere durable to live.
type shemRepoPaths struct {
	Services map[string]struct {
		Volumes []string `yaml:"volumes"`
	} `yaml:"services"`
	Volumes map[string]any `yaml:"volumes"`
}

type shemConfig struct {
	Repos []struct {
		Path   string `yaml:"path"`
		Remote string `yaml:"remote"`
	} `yaml:"repos"`
}

// Every repository in the shem's config must resolve to a mount, or its
// checkout lives in the container's own writable layer.
//
// deploy/shem.yaml named /repos/omnicore-platform while docker-compose.yml
// mounted only /repos/test and /repos/golem, so the clone, the .golem ticket
// state and the worktrees were all discarded on the next container recreate
// and the entire repository was cloned again. Nothing failed visibly — the
// work simply disappeared — which is why this is a test and not a comment.
func TestEveryShemRepoHasAMount(t *testing.T) {
	var compose shemRepoPaths
	if err := yaml.Unmarshal([]byte(repoFile(t, "docker-compose.yml")), &compose); err != nil {
		t.Fatalf("parse docker-compose.yml: %v", err)
	}
	var cfg shemConfig
	if err := yaml.Unmarshal([]byte(repoFile(t, "deploy/shem.yaml")), &cfg); err != nil {
		t.Fatalf("parse deploy/shem.yaml: %v", err)
	}
	if len(cfg.Repos) == 0 {
		t.Fatal("deploy/shem.yaml declares no repos; this test would pass vacuously")
	}

	// Container-side mount targets the shem has, e.g. "/repos", "/repos/test".
	var targets []string
	for _, v := range compose.Services["shem"].Volumes {
		parts := strings.Split(v, ":")
		if len(parts) < 2 {
			continue
		}
		targets = append(targets, parts[1])
		// A named volume must also be declared, or compose refuses to start.
		if !strings.HasPrefix(parts[0], ".") && !strings.HasPrefix(parts[0], "/") &&
			!strings.HasPrefix(parts[0], "$") {
			if _, ok := compose.Volumes[parts[0]]; !ok {
				t.Errorf("shem mounts named volume %q, which is not declared under volumes:", parts[0])
			}
		}
	}

	for _, repo := range cfg.Repos {
		covered := false
		for _, target := range targets {
			if repo.Path == target || strings.HasPrefix(repo.Path, strings.TrimSuffix(target, "/")+"/") {
				covered = true
				break
			}
		}
		if !covered {
			t.Errorf("shem.yaml repo %q (%s) is under no mount in docker-compose.yml: "+
				"its checkout would live in the container's writable layer and be lost "+
				"on the next recreate. Mount a volume at /repos or at that path.",
				repo.Path, repo.Remote)
		}
	}
}

// shemInit is the subset needed to check PID-1 reaping. `init` is a compose
// tri-state: absent means "inherit the daemon default", which is off unless
// the operator changed it, so a *pointer* is needed to tell "not set" from
// "set false".
type shemInit struct {
	Services map[string]struct {
		Build struct {
			Target string `yaml:"target"`
		} `yaml:"build"`
		Init *bool `yaml:"init"`
	} `yaml:"services"`
}

// Every service running the shem image must run under an init process.
//
// golem-shem is PID 1 there, and the agent's process tree — claude, git, npm,
// hooks — reparents its orphans onto PID 1. os/exec reaps only the children Go
// itself started, so everything else piles up as a zombie: 77 of them in about
// two hours of ticket work on the first real deployment. That ends in PID
// exhaustion, at which point the container cannot fork and every phase fails.
//
// Checked by build target rather than by service name so that the second and
// third shem an operator adds for concurrency are covered too — the failure is
// invisible until the container is wedged, which is the worst time to find out
// the new service was missing a line the first one had.
func TestShemServicesRunUnderAnInit(t *testing.T) {
	var compose shemInit
	if err := yaml.Unmarshal([]byte(repoFile(t, "docker-compose.yml")), &compose); err != nil {
		t.Fatalf("parse docker-compose.yml: %v", err)
	}

	checked := 0
	for name, svc := range compose.Services {
		if svc.Build.Target != "shem" {
			continue
		}
		// repo-init builds the shem image only to borrow git; it runs one
		// shell command and exits, and spawns no agent tree.
		if name == "repo-init" {
			continue
		}
		checked++
		if svc.Init == nil || !*svc.Init {
			t.Errorf("service %q builds the shem image but does not set init: true; "+
				"golem-shem would be PID 1 and would not reap the agent's orphaned "+
				"grandchildren, leaking zombies until the container hits its PID limit", name)
		}
	}
	if checked == 0 {
		t.Fatal("no service builds target shem; this test would pass vacuously")
	}
}

// portsFile is the subset needed to reason about which port the orchestrator
// is published on and which one the stack talks to internally.
type portsFile struct {
	Services map[string]struct {
		Ports       []string `yaml:"ports"`
		Environment []string `yaml:"environment"`
		Healthcheck struct {
			Test []string `yaml:"test"`
		} `yaml:"healthcheck"`
	} `yaml:"services"`
}

// The published host port must be settable from .env, and the internal port
// must not move with it.
//
// Only the host side can clash: an operator with something else on 8080
// cannot start the stack at all. Inside the container network nothing
// competes for the port, so there is no reason to move it there — and three
// things would have to move together if it did (the container side of this
// mapping, the healthcheck, and the orchestrator URL in shem.yaml). Passing
// GOLEM_PORT into the orchestrator's own environment is the specific mistake
// this pins: the server would then listen on the new port while the
// healthcheck kept probing 8080, so the container would be restarted forever
// as unhealthy and the shem could never reach it.
func TestOrchestratorPortIsConfigurableWithoutMovingTheInternalPort(t *testing.T) {
	var compose portsFile
	if err := yaml.Unmarshal([]byte(repoFile(t, "docker-compose.yml")), &compose); err != nil {
		t.Fatalf("parse docker-compose.yml: %v", err)
	}
	orch, ok := compose.Services["orchestrator"]
	if !ok {
		t.Fatal("no orchestrator service in docker-compose.yml")
	}
	if len(orch.Ports) != 1 {
		t.Fatalf("orchestrator publishes %d port mappings, want exactly 1: %v", len(orch.Ports), orch.Ports)
	}

	// Split on the LAST colon: the host side is "${GOLEM_PORT:-8080}", which
	// contains a colon of its own inside the shell default syntax.
	idx := strings.LastIndex(orch.Ports[0], ":")
	if idx < 0 {
		t.Fatalf("port mapping %q is not host:container", orch.Ports[0])
	}
	host, container := orch.Ports[0][:idx], orch.Ports[0][idx+1:]
	if !strings.Contains(host, "${GOLEM_PORT") {
		t.Errorf("the published host port is %q, which no environment variable can move; "+
			"an operator already running something on that port cannot start the stack", host)
	}
	if !strings.Contains(host, ":-8080") {
		t.Errorf("host port %q has no 8080 default; an operator with no GOLEM_PORT set "+
			"would get an empty mapping", host)
	}
	if container != "8080" {
		t.Errorf("container side of the mapping is %q, want 8080", container)
	}

	// GOLEM_PORT must not reach the orchestrator process.
	if v, ok := envValue(orch.Environment, "GOLEM_PORT"); ok {
		t.Errorf("GOLEM_PORT=%q is passed into the orchestrator container; it would listen "+
			"on that port while the healthcheck and the shem still use %s", v, container)
	}

	// The healthcheck probes from inside the container, so it must use the
	// internal port, not the published one.
	probe := strings.Join(orch.Healthcheck.Test, " ")
	if probe == "" {
		t.Fatal("orchestrator has no healthcheck")
	}
	if !strings.Contains(probe, "localhost:"+container) {
		t.Errorf("healthcheck %q does not probe localhost:%s; it will report the container "+
			"unhealthy forever and compose will never start the shem", probe, container)
	}

	// And the shem reaches the orchestrator over the container network, so
	// it must agree on the same internal port.
	shemYAML := repoFile(t, "deploy/shem.yaml")
	if !strings.Contains(shemYAML, "http://orchestrator:"+container) {
		t.Errorf("deploy/shem.yaml does not point at orchestrator:%s; the shem would never "+
			"connect", container)
	}
}

// Every operator setting the orchestrator reads from its environment must
// actually be passed into its container.
//
// GOLEM_PR_FIX_ATTEMPTS was added to the config code and documented in
// .env.example, and never wired into docker-compose.yml. Inside the
// container it was simply unset, so the code fell back to its default and
// the setting did nothing at all in the only deployment anyone uses —
// including the 0 that is supposed to disable automatic fixing entirely.
// Nothing failed; the value in .env was read by compose, matched no
// variable, and was dropped.
//
// The list is derived from the config package's own constants rather than
// written out here, so the next setting added is covered without anyone
// remembering to extend this test.
func TestOrchestratorReceivesEveryEnvSettingItReads(t *testing.T) {
	src := repoFile(t, "internal/orchestrator/config/config.go")
	names := regexp.MustCompile(`"(GOLEM_[A-Z_]+)"`).FindAllStringSubmatch(src, -1)
	if len(names) == 0 {
		t.Fatal("no GOLEM_* env names found in config.go; this test would pass vacuously")
	}

	var compose portsFile
	if err := yaml.Unmarshal([]byte(repoFile(t, "docker-compose.yml")), &compose); err != nil {
		t.Fatalf("parse docker-compose.yml: %v", err)
	}
	env := compose.Services["orchestrator"].Environment

	// GOLEM_PORT is the one deliberate omission: it moves the PUBLISHED
	// port only, and passing it in would move the container's listener away
	// from the port the healthcheck and the shem both use. That exception
	// is pinned by its own test.
	exempt := map[string]bool{"GOLEM_PORT": true}

	seen := map[string]bool{}
	for _, m := range names {
		name := m[1]
		if exempt[name] || seen[name] {
			continue
		}
		seen[name] = true
		if _, ok := envValue(env, name); !ok {
			t.Errorf("config.go reads %s but docker-compose.yml never passes it to the "+
				"orchestrator; in the container it is unset and the setting silently "+
				"does nothing", name)
		}
	}
}
