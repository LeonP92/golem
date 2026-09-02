package graph

import (
	"testing"
)

func TestParse_happyPath(t *testing.T) {
	output := `GRAPH_MODULE:src/auth
GRAPH_SUMMARY:Handles authentication and session management.
GRAPH_EXPORT_FN:validate_token(token: str) -> Claims — validates JWT
GRAPH_EXPORT_TYPE:Claims — JWT payload
GRAPH_IMPORTS:src/models,src/config
GRAPH_CALLS:models.get_user
GRAPH_SUBSYSTEM:auth`

	g := Parse(output)
	if g.Module != "src/auth" {
		t.Errorf("Module = %q, want src/auth", g.Module)
	}
	if g.Summary != "Handles authentication and session management." {
		t.Errorf("Summary = %q", g.Summary)
	}
	if len(g.ExportFns) != 1 {
		t.Fatalf("ExportFns len = %d, want 1", len(g.ExportFns))
	}
	if len(g.ExportTypes) != 1 {
		t.Errorf("ExportTypes len = %d, want 1", len(g.ExportTypes))
	}
	if len(g.Imports) != 2 {
		t.Errorf("Imports = %v, want 2", g.Imports)
	}
	if g.Subsystem != "auth" {
		t.Errorf("Subsystem = %q, want auth", g.Subsystem)
	}
}

func TestParse_missingTags(t *testing.T) {
	g := Parse("GRAPH_MODULE:x\nGRAPH_SUMMARY:minimal")
	if g.Module != "x" || g.Summary != "minimal" {
		t.Error("expected minimal parse to work")
	}
	if len(g.ExportFns) != 0 || len(g.ExportTypes) != 0 {
		t.Error("expected empty slices for missing tags")
	}
}

func TestParse_multipleExports(t *testing.T) {
	output := "GRAPH_MODULE:m\nGRAPH_SUMMARY:s\nGRAPH_EXPORT_FN:fn1\nGRAPH_EXPORT_FN:fn2\nGRAPH_EXPORT_TYPE:T1\nGRAPH_EXPORT_TYPE:T2"
	g := Parse(output)
	if len(g.ExportFns) != 2 {
		t.Errorf("ExportFns = %d, want 2", len(g.ExportFns))
	}
	if len(g.ExportTypes) != 2 {
		t.Errorf("ExportTypes = %d, want 2", len(g.ExportTypes))
	}
}

func TestSplitCSV(t *testing.T) {
	got := splitCSV("a, b , c")
	if len(got) != 3 || got[0] != "a" || got[1] != "b" || got[2] != "c" {
		t.Errorf("splitCSV = %v", got)
	}
	if splitCSV("") != nil {
		t.Error("empty string should return nil")
	}
}
