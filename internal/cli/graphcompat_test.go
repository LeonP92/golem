package cli

import (
	"bytes"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/leonp92/golem/internal/agentrunner"
	"github.com/leonp92/golem/internal/graph"
)

// Registered once: Register panics on a duplicate, which a per-test call hits under -count=2.
func init() {
	agentrunner.Register("compat-no-model", func(agentrunner.Options) agentrunner.Adapter { return &fakeRunner{output: goodNarrative} })
}

func compatGit(t *testing.T, repo string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "user.name=Test", "-c", "user.email=test@local"}, args...)...)
	cmd.Dir = repo
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %s %v", args, out, err)
	}
}

// compatRepo returns a committed repo with a golem config using the no-model backend.
func compatRepo(t *testing.T, files map[string]string) string {
	t.Helper()
	repo := t.TempDir()
	compatGit(t, repo, "init")
	for name, src := range files {
		writeFile(t, filepath.Join(repo, name), src)
	}
	writeFile(t, filepath.Join(repo, ".golem", "config.yaml"), "backend: compat-no-model\n")
	compatGit(t, repo, "add", ".")
	compatGit(t, repo, "commit", "-m", "init")
	return repo
}

func commitFile(t *testing.T, repo, name, src string) {
	t.Helper()
	writeFile(t, filepath.Join(repo, name), src)
	compatGit(t, repo, "add", name)
	compatGit(t, repo, "commit", "-m", "change "+name)
}

func runGraph(t *testing.T, cmd func([]string, io.Writer, io.Writer) int, repo string) {
	t.Helper()
	var out, errb bytes.Buffer
	if code := cmd([]string{"--repo", repo}, &out, &errb); code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
}

func readGraphFile(t *testing.T, repo, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(repo, ".golem", "wiki", "graph", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// An index written by golem before ranges existed updates in place: the
// changed module gains a location, the untouched one keeps its old record.
func TestGraphUpdateLegacyChangedModuleLazy(t *testing.T) {
	repo := compatRepo(t, map[string]string{"a.go": "package p\nfunc Run() {}\n"})
	idx := filepath.Join(repo, ".golem", "index")
	head, _ := gitHead(repo)
	if err := graph.SaveMeta(idx, &graph.Meta{Version: graph.MetaVersion, BaseCommit: head}); err != nil {
		t.Fatal(err)
	}
	for _, g := range []graph.ModuleGraph{{Module: ".", ExportFns: []string{"Old"}}, {Module: "untouched", ExportFns: []string{"Legacy"}}} {
		if err := graph.SaveModuleGraph(idx, g); err != nil {
			t.Fatal(err)
		}
	}
	commitFile(t, repo, "a.go", "package p\n// docs\nfunc Run() {}\n")
	runGraph(t, GraphUpdate, repo)

	symbols := readGraphFile(t, repo, "symbols.md")
	for _, want := range []string{"- `.` (`a.go`) [L2-L3] — Run\n", "- `untouched` — Legacy\n"} {
		if !strings.Contains(symbols, want) {
			t.Errorf("symbols.md missing %q:\n%s", want, symbols)
		}
	}
}

// Shifting a declaration's lines in a committed change must refresh its
// range on update, while other modules keep theirs.
func TestGraphUpdateRefreshesShiftedRanges(t *testing.T) {
	repo := compatRepo(t, map[string]string{
		"a/a.go": "package a\nfunc Run() {}\ntype Config struct{}\n",
		"b/b.go": "package b\nfunc Other() {}\n",
	})
	runGraph(t, GraphBuild, repo)
	before := readGraphFile(t, repo, "symbols.md")
	if !strings.Contains(before, "(`a/a.go`) [L2-L2] — Run") {
		t.Fatalf("build missing range:\n%s", before)
	}

	commitFile(t, repo, "a/a.go", "package a\n\nimport \"fmt\"\n\nvar _ = fmt.Sprint\n\nfunc Run() {\n}\ntype Config struct{}\n")
	runGraph(t, GraphUpdate, repo)

	symbols, types := readGraphFile(t, repo, "symbols.md"), readGraphFile(t, repo, "types.md")
	for _, want := range []string{"(`a/a.go`) [L7-L8] — Run", "(`b/b.go`) [L2-L2] — Other"} {
		if !strings.Contains(symbols, want) {
			t.Errorf("symbols.md missing %q:\n%s", want, symbols)
		}
	}
	if !strings.Contains(types, "(`a/a.go`) [L9-L9] — Config") {
		t.Errorf("types.md not refreshed:\n%s", types)
	}
	if strings.Contains(symbols, "[L2-L2] — Run") {
		t.Errorf("stale range kept:\n%s", symbols)
	}
}
