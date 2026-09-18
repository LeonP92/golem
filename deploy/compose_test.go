package deploy

import (
	"os"
	"path/filepath"
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
