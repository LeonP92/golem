package graph

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSlug(t *testing.T) {
	for path, prefix := range map[string]string{"src/auth": "src_auth-", "internal/cli": "internal_cli-", ".": "_-"} {
		if got := Slug(path); !strings.HasPrefix(got, prefix) || len(got) != len(prefix)+8 {
			t.Errorf("Slug(%q) = %q, want %q + 8 hex chars", path, got, prefix)
		}
	}
	if Slug("test/e2e/dra") == Slug("test/e2e_dra") {
		t.Error("paths that flatten alike must not share a slug")
	}
	// Bound to names rather than compared inline: two identical expressions
	// either side of != read as a typo to staticcheck (SA4000), though the
	// intent — that two separate calls agree — is real.
	first, second := Slug("a/b"), Slug("a/b")
	if first != second {
		t.Errorf("Slug must be deterministic: %q then %q", first, second)
	}
}

func TestWriteModule_idempotent(t *testing.T) {
	wikiDir := t.TempDir()
	g := ModuleGraph{
		Module:      "src/auth",
		Summary:     "Auth module.",
		ExportFns:   []string{"Login() error — logs in"},
		ExportTypes: []string{"User — represents a user"},
		Imports:     []string{"src/db"},
		Subsystem:   "auth",
	}
	if err := WriteModule(wikiDir, g); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(wikiDir, "graph", "modules", Slug(g.Module)+".md")
	first, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	if err := WriteModule(wikiDir, g); err != nil {
		t.Fatal(err)
	}
	second, _ := os.ReadFile(path)
	if string(first) != string(second) {
		t.Error("WriteModule should be idempotent")
	}
}

func TestWriteModule_richFields(t *testing.T) {
	wikiDir := t.TempDir()
	g := ModuleGraph{
		Module:     "internal/graph",
		PackageDoc: "Graph package handles ...",
		Subsystem:  "graph",
		ExportedFuncs: []ExportedFunc{
			{Name: "Extract", Signature: "func Extract(src []byte, lang string)", Doc: "Extract runs tree-sitter."},
		},
		ExportedTypes: []ExportedType{
			{Name: "ModuleGraph", Doc: "ModuleGraph is the record.", Fields: []string{"Module string"}},
		},
		Consts:  []string{"MaxSize"},
		Imports: []string{"fmt"},
	}
	if err := WriteModule(wikiDir, g); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(wikiDir, "graph", "modules", Slug(g.Module)+".md"))
	if err != nil {
		t.Fatal(err)
	}
	s := string(data)
	for _, want := range []string{
		"# internal/graph",
		"Graph package handles",
		"Part of subsystem: [graph]",
		"### `Extract`",
		"func Extract(src []byte, lang string)",
		"Extract runs tree-sitter.",
		"### `ModuleGraph`",
		"Module string",
		"## Constants",
		"`MaxSize`",
		"## Imports",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("output missing %q\nfull:\n%s", want, s)
		}
	}
}

func TestWriteIndex_narratives(t *testing.T) {
	wikiDir := t.TempDir()
	graphs := []ModuleGraph{
		{Module: "internal/graph", Subsystem: "graph", PackageDoc: "graph doc"},
	}
	narr := SubsystemNarratives{"graph": "The graph subsystem does X."}
	if err := WriteIndex(wikiDir, graphs, narr); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(filepath.Join(wikiDir, "graph", "index.md"))
	s := string(data)
	if !strings.Contains(s, "## graph") {
		t.Errorf("missing graph header: %s", s)
	}
	if !strings.Contains(s, "The graph subsystem does X.") {
		t.Errorf("missing narrative: %s", s)
	}
	if !strings.Contains(s, "graph doc") {
		t.Errorf("missing package doc summary: %s", s)
	}
}

func TestWriteIndex_groupsBySubsystem(t *testing.T) {
	wikiDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(wikiDir, "graph"), 0o755); err != nil {
		t.Fatal(err)
	}
	graphs := []ModuleGraph{
		{Module: "src/auth", Summary: "Auth.", Subsystem: "auth"},
		{Module: "src/billing", Summary: "Billing.", Subsystem: "billing"},
		{Module: "src/login", Summary: "Login.", Subsystem: "auth"},
	}
	if err := WriteIndex(wikiDir, graphs, nil); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(filepath.Join(wikiDir, "graph", "index.md"))
	content := string(data)
	authPos := strings.Index(content, "## auth")
	billingPos := strings.Index(content, "## billing")
	if authPos == -1 || billingPos == -1 {
		t.Error("expected both subsystem headers")
	}
	if authPos > billingPos {
		t.Error("expected auth before billing (alphabetical)")
	}
}

func TestHeadingAnchor(t *testing.T) {
	cases := map[string]string{
		"orchestrator":      "orchestrator",
		"client-go/dynamic": "client-godynamic",
		"API Server":        "api-server",
		"k8s_io.util":       "k8s_ioutil",
	}
	for in, want := range cases {
		if got := headingAnchor(in); got != want {
			t.Errorf("headingAnchor(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestAppendIndexesSourceFiles(t *testing.T) {
	wiki := t.TempDir()
	if err := ResetAggregates(wiki); err != nil {
		t.Fatal(err)
	}
	g := ModuleGraph{Module: "pkg", ExportedFuncs: []ExportedFunc{
		{Name: "Run", File: "pkg/a.go"}, {Name: "Run", File: "pkg/b.go"},
	}, ExportedTypes: []ExportedType{{Name: "Config", File: "pkg/types.go"}}}
	if err := AppendSymbols(wiki, g); err != nil {
		t.Fatal(err)
	}
	if err := AppendTypes(wiki, g); err != nil {
		t.Fatal(err)
	}
	for file, paths := range map[string][]string{"symbols.md": {"pkg/a.go", "pkg/b.go"}, "types.md": {"pkg/types.go"}} {
		data, err := os.ReadFile(filepath.Join(wiki, "graph", file))
		if err != nil {
			t.Fatal(err)
		}
		for _, path := range paths {
			if !strings.Contains(string(data), "`"+path+"`") {
				t.Errorf("missing %s: %s", path, data)
			}
		}
	}
	// Old JSON/name-only records must still render, without invented file paths.
	if err := AppendSymbols(wiki, ModuleGraph{Module: "old", ExportFns: []string{"Legacy"}}); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(filepath.Join(wiki, "graph", "symbols.md"))
	if !strings.Contains(string(data), symbolLine("old", "Legacy", "", 0, 0)) {
		t.Fatal("legacy fallback missing")
	}
}

func TestSymbolLineBackwardCompatibleRanges(t *testing.T) {
	for _, tc := range []struct {
		file       string
		start, end int
		want       string
	}{
		{"", 0, 0, "- `pkg` — Run\n"},
		{"pkg/a.go", 0, 0, "- `pkg` (`pkg/a.go`) — Run\n"},
		{"pkg/a.go", 2, 5, "- `pkg` (`pkg/a.go`) [L2-L5] — Run\n"},
		{"pkg/a.go", 5, 2, "- `pkg` (`pkg/a.go`) — Run\n"},
	} {
		if got := symbolLine("pkg", "Run", tc.file, tc.start, tc.end); got != tc.want {
			t.Fatalf("got %q want %q", got, tc.want)
		}
	}
}
