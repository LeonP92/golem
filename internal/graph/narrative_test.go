package graph

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestClusterHash_deterministicAndSensitive(t *testing.T) {
	cluster := []ModuleGraph{
		{Module: "a/b", PackageDoc: "doc1"},
		{Module: "a/c", PackageDoc: "doc2"},
	}
	h1 := ClusterHash("sub", cluster)
	h2 := ClusterHash("sub", cluster)
	if h1 != h2 {
		t.Errorf("ClusterHash not deterministic: %s vs %s", h1, h2)
	}
	cluster[0].PackageDoc = "changed"
	h3 := ClusterHash("sub", cluster)
	if h3 == h1 {
		t.Error("ClusterHash should change when PackageDoc changes")
	}
}

func TestNarrativeCache_roundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "n.json")
	c, err := LoadNarrativeCache(path)
	if err != nil {
		t.Fatal(err)
	}
	c.Set("sub", "hash1", "The sub does foo.")
	if err := c.Save(); err != nil {
		t.Fatal(err)
	}
	c2, err := LoadNarrativeCache(path)
	if err != nil {
		t.Fatal(err)
	}
	got, _, ok := c2.Lookup("hash1")
	if !ok || got != "The sub does foo." {
		t.Errorf("got %q ok=%v", got, ok)
	}
}

func TestValidateNarrative(t *testing.T) {
	cluster := []ModuleGraph{
		{Module: "internal/graph"},
		{Module: "internal/cli"},
	}
	// Too short.
	if err := ValidateNarrative("short", cluster); err == nil {
		t.Error("expected error on short narrative")
	}
	// Forbidden phrase.
	bad := strings.Repeat("This subsystem contains modules for graph and cli work. ", 5)
	if err := ValidateNarrative(bad, cluster); err == nil {
		t.Error("expected error on forbidden phrase")
	}
	// Too few refs (only "graph" appears).
	few := strings.Repeat("The graph package parses trees. ", 5)
	if err := ValidateNarrative(few, cluster); err == nil {
		t.Error("expected error on too few module refs")
	}
	// Success: references both leaf names, no forbidden phrase, >100 chars.
	good := "The graph module drives tree-sitter extraction while the cli module wires user-facing commands into that pipeline for wiki generation."
	if err := ValidateNarrative(good, cluster); err != nil {
		t.Errorf("unexpected: %v", err)
	}
	// Single-module clusters only need one reference.
	solo := []ModuleGraph{{Module: "internal/graph"}}
	oneRef := "The graph module handles all tree-sitter extraction plumbing needed to produce deterministic wiki pages across every supported language."
	if err := ValidateNarrative(oneRef, solo); err != nil {
		t.Errorf("single-module cluster should accept 1-ref: %v", err)
	}
}

func TestExtractNarrativeJSON(t *testing.T) {
	raw := "Sure! Here's the JSON:\n\n{\"subsystem\":\"graph\",\"narrative\":\"Handles code parsing.\"}\n\nThanks."
	got := ExtractNarrativeJSON(raw)
	if got != "Handles code parsing." {
		t.Errorf("ExtractNarrativeJSON = %q", got)
	}
	// Fallback when no JSON present.
	plain := "just a raw sentence"
	if ExtractNarrativeJSON(plain) != plain {
		t.Error("expected raw fallback")
	}
}

func TestClusterBySubsystem_stable(t *testing.T) {
	graphs := []ModuleGraph{
		{Module: "internal/cli", Subsystem: "cli"},
		{Module: "internal/graph", Subsystem: "graph"},
		{Module: "cmd/golem", Subsystem: "golem"},
		{Module: "internal/graph/queries", Subsystem: "graph"},
	}
	out := ClusterBySubsystem(graphs)
	if len(out["graph"]) != 2 {
		t.Errorf("graph cluster = %v", out["graph"])
	}
	if out["graph"][0].Module != "internal/graph" {
		t.Errorf("graph cluster not sorted: %+v", out["graph"])
	}
}

func TestNarrativeCache_failedAndPrune(t *testing.T) {
	path := filepath.Join(t.TempDir(), "n.json")
	c, _ := LoadNarrativeCache(path)
	c.Set("a", "h1", "narrative a")
	c.SetFailed("b", "h2")
	c.Prune(map[string]bool{"h2": true})
	if err := c.Save(); err != nil {
		t.Fatal(err)
	}
	c2, err := LoadNarrativeCache(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, ok := c2.Lookup("h1"); ok {
		t.Error("h1 should have been pruned")
	}
	if _, failed, ok := c2.Lookup("h2"); !ok || !failed {
		t.Errorf("h2: failed=%v ok=%v, want cached failure", failed, ok)
	}
}
