package graph

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSlug(t *testing.T) {
	cases := [][2]string{
		{"src/auth", "src_auth"},
		{"internal/cli", "internal_cli"},
		{".", "_"},
	}
	for _, c := range cases {
		if got := Slug(c[0]); got != c[1] {
			t.Errorf("Slug(%q) = %q, want %q", c[0], got, c[1])
		}
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
	path := filepath.Join(wikiDir, "graph", "modules", "src_auth.md")
	first, _ := os.ReadFile(path)

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
	data, _ := os.ReadFile(filepath.Join(wikiDir, "graph", "modules", "internal_graph.md"))
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
