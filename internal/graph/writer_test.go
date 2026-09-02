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
		if got := slug(c[0]); got != c[1] {
			t.Errorf("slug(%q) = %q, want %q", c[0], got, c[1])
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
	if err := WriteIndex(wikiDir, graphs); err != nil {
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
