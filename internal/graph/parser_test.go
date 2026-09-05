package graph

import (
	"testing"
)

func TestParse_happyPath(t *testing.T) {
	input := `
GRAPH_SUMMARY:Handles JWT validation and session creation.
GRAPH_SUBSYSTEM:auth
`
	got := Parse(input)
	if got.Summary != "Handles JWT validation and session creation." {
		t.Errorf("Summary = %q", got.Summary)
	}
	if got.Subsystem != "auth" {
		t.Errorf("Subsystem = %q", got.Subsystem)
	}
	// Structural fields are empty — they come from tree-sitter, not Parse.
	if len(got.Imports) != 0 || len(got.ExportFns) != 0 {
		t.Error("Parse should not populate structural fields")
	}
}

func TestParse_missingTags(t *testing.T) {
	got := Parse("nothing useful here")
	if got.Summary != "" || got.Subsystem != "" {
		t.Errorf("expected empty semantic fields, got %+v", got)
	}
}

func TestParse_ignoresStructuralTags(t *testing.T) {
	// Even if the LLM still outputs old structural tags, Parse ignores them.
	input := `
GRAPH_EXPORT_FN:DoThing
GRAPH_IMPORTS:internal/foo
GRAPH_SUMMARY:Does a thing.
GRAPH_SUBSYSTEM:core
`
	got := Parse(input)
	if len(got.ExportFns) != 0 || len(got.Imports) != 0 {
		t.Error("structural tags must be ignored by Parse")
	}
}

func TestParse_moduleStillParsed(t *testing.T) {
	input := `
GRAPH_MODULE:src/auth
GRAPH_SUMMARY:Auth module summary.
GRAPH_SUBSYSTEM:auth
`
	got := Parse(input)
	if got.Module != "src/auth" {
		t.Errorf("Module = %q, want src/auth", got.Module)
	}
	if got.Summary != "Auth module summary." {
		t.Errorf("Summary = %q", got.Summary)
	}
}
