package cli

import (
	"bytes"
	"context"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/leonp92/golem/internal/agentrunner"
	"github.com/leonp92/golem/internal/models"
)

// probeSession is one recorded RunPhase call.
type probeSession struct {
	dir  string
	mode fs.FileMode
}

// probeSessions is written by the probe-test adapter below. Tests in this
// package run sequentially, and each clears it first.
var probeSessions []probeSession

// probeAdapter answers every model but those its env block names, and records
// the directory and mode each session ran in.
type probeAdapter struct {
	agentrunner.Adapter
	failing map[string]bool
}

func (probeAdapter) Name() string                   { return "probe-test" }
func (probeAdapter) ReservedArgs() []string         { return nil }
func (probeAdapter) DefaultCatalog() models.Catalog { return models.Catalog{} }

func (p probeAdapter) RunPhase(_ context.Context, dir, _ string, _ io.Writer, model string) error {
	rec := probeSession{dir: dir}
	if info, err := os.Stat(dir); err == nil {
		rec.mode = info.Mode().Perm()
	}
	probeSessions = append(probeSessions, rec)
	if p.failing[model] {
		return io.ErrUnexpectedEOF
	}
	return nil
}

func init() {
	agentrunner.Register("probe-test", func(o agentrunner.Options) agentrunner.Adapter {
		failing := map[string]bool{}
		for _, id := range strings.Split(o.Env["PROBE_FAIL"], ",") {
			if id != "" {
				failing[id] = true
			}
		}
		return probeAdapter{failing: failing}
	})
}

// probeDoc is a backend document for the probe-test adapter, failing the
// named model ids.
func probeDoc(t *testing.T, failing string) string {
	t.Helper()
	probeSessions = nil
	return writeBackendDoc(t, `
backend:
  adapter: probe-test
  env:
    PROBE_FAIL: "`+failing+`"
  supports_selection: true
  tiers: [small, medium, large]
  models:
    - {id: haiku,  label: "Haiku",  tier: small}
    - {id: sonnet, label: "Sonnet", tier: medium}
    - {id: opus,   label: "Opus",   tier: large}
`)
}

func TestModelsListPrintsTheCatalogAndStageDefaults(t *testing.T) {
	doc := writeBackendDoc(t, "backend:\n  adapter: claude-code\n")
	var stdout, stderr bytes.Buffer
	if code := ModelsList([]string{"--backend-config", doc}, &stdout, &stderr); code != 0 {
		t.Fatalf("ModelsList: exit %d, stderr=%s", code, stderr.String())
	}
	out := stdout.String()
	seed := agentrunner.ResolveBackend(nil)
	for _, m := range seed.Models {
		if !strings.Contains(out, m.ID) {
			t.Errorf("output does not list model %q:\n%s", m.ID, out)
		}
		if !strings.Contains(out, string(m.Tier)) {
			t.Errorf("output does not list tier %q", m.Tier)
		}
	}
	resolved, _ := models.Resolve(seed.Catalog, nil)
	for _, st := range models.Stages {
		if !strings.Contains(out, string(st)) {
			t.Errorf("output does not name stage %q", st)
		}
		if !strings.Contains(out, resolved[st]) {
			t.Errorf("output does not carry %q's default %q", st, resolved[st])
		}
	}
}

func TestModelsListReadsAShemYAML(t *testing.T) {
	t.Run("a declared catalog", func(t *testing.T) {
		path := writeBackendDoc(t, `
orchestrator: https://golem.example.com
backend:
  models:
    - {id: house-model, label: "The house model"}
`)
		var stdout, stderr bytes.Buffer
		if code := ModelsList([]string{"--config", path}, &stdout, &stderr); code != 0 {
			t.Fatalf("ModelsList: exit %d, stderr=%s", code, stderr.String())
		}
		if !strings.Contains(stdout.String(), "house-model") {
			t.Errorf("output does not list the declared model:\n%s", stdout.String())
		}
	})

	t.Run("no backend block gives the seed", func(t *testing.T) {
		path := writeBackendDoc(t, "orchestrator: https://golem.example.com\n")
		var stdout, stderr bytes.Buffer
		if code := ModelsList([]string{"--config", path}, &stdout, &stderr); code != 0 {
			t.Fatalf("ModelsList: exit %d, stderr=%s", code, stderr.String())
		}
		if !strings.Contains(stdout.String(), "haiku") {
			t.Errorf("output does not carry the seed catalog:\n%s", stdout.String())
		}
	})
}

func TestModelsRequiresAConfigFlag(t *testing.T) {
	for name, run := range map[string]func([]string, io.Writer, io.Writer) int{
		"list": ModelsList, "probe": ModelsProbe,
	} {
		t.Run(name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if code := run(nil, &stdout, &stderr); code == 0 {
				t.Fatalf("models %s with neither flag exited 0", name)
			}
			for _, want := range []string{"--config", "--backend-config"} {
				if !strings.Contains(stderr.String(), want) {
					t.Errorf("stderr %q does not name %q", stderr.String(), want)
				}
			}
		})
	}
}

func TestModelsProbeKeepsOnlyTheModelsThatAnswered(t *testing.T) {
	doc := probeDoc(t, "sonnet")
	var stdout, stderr bytes.Buffer
	if code := ModelsProbe([]string{"--backend-config", doc}, &stdout, &stderr); code != 0 {
		t.Fatalf("ModelsProbe: exit %d, stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "sonnet") {
		t.Errorf("stderr does not name the failing model:\n%s", stderr.String())
	}

	// The printed block is what an operator pastes back, so it must parse.
	printed := filepath.Join(t.TempDir(), "printed.yaml")
	if err := os.WriteFile(printed, stdout.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	back, err := agentrunner.LoadBackendConfig(printed)
	if err != nil {
		t.Fatalf("the printed block does not load: %v", err)
	}
	var ids []string
	for _, m := range back.Models {
		ids = append(ids, m.ID)
	}
	if strings.Join(ids, ",") != "haiku,opus" {
		t.Errorf("the printed block's models = %v, want haiku and opus", ids)
	}
}

func TestModelsProbeSessionDirectoryIsTemporaryAndAgentReadable(t *testing.T) {
	doc := probeDoc(t, "")
	var stdout, stderr bytes.Buffer
	if code := ModelsProbe([]string{"--backend-config", doc}, &stdout, &stderr); code != 0 {
		t.Fatalf("ModelsProbe: exit %d, stderr=%s", code, stderr.String())
	}
	if len(probeSessions) != 3 {
		t.Fatalf("%d sessions ran, want 3", len(probeSessions))
	}
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range probeSessions {
		if s.dir == cwd {
			t.Error("a probe session ran in the working directory")
		}
		// DropPrivileges runs the vendor CLI as the agent account, which has
		// to be able to enter this directory.
		if runtime.GOOS != "windows" && s.mode != 0o755 {
			t.Errorf("session directory mode = %v, want 0755", s.mode)
		}
		if _, err := os.Stat(s.dir); err == nil {
			t.Errorf("the session directory %s outlived the command", s.dir)
		}
	}
}

func TestModelsProbeWithNoWorkingModelPrintsNothing(t *testing.T) {
	doc := probeDoc(t, "haiku,sonnet,opus")
	var stdout, stderr bytes.Buffer
	if code := ModelsProbe([]string{"--backend-config", doc}, &stdout, &stderr); code == 0 {
		t.Fatal("a probe with no verified model exited 0")
	}
	if stdout.Len() != 0 {
		t.Errorf("stdout is not empty: %q", stdout.String())
	}
	for _, want := range []string{"haiku", "sonnet", "opus"} {
		if !strings.Contains(stderr.String(), want) {
			t.Errorf("stderr does not name the failing model %q:\n%s", want, stderr.String())
		}
	}
}

func TestModelsListNamesTheVendorDefault(t *testing.T) {
	doc := writeBackendDoc(t, "backend:\n  tiers: [small]\n  models:\n    - {id: only, tier: small}\n  stage_defaults: {review: small}\n")
	var stdout, stderr bytes.Buffer
	if code := ModelsList([]string{"--backend-config", doc}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "(vendor default)") {
		t.Errorf("a stage with no default is not labelled:\n%s", stdout.String())
	}
}
