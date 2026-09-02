package wiki

import (
	"os"
	"path/filepath"
	"testing"
)

func TestBuildLinksDetectsCoMentions(t *testing.T) {
	docs := []Doc{
		{Path: "modules/auth.md", Content: "handles authentication and delegates token storage to session"},
		{Path: "modules/session.md", Content: "stores session tokens in memory"},
		{Path: "modules/billing.md", Content: "calculates invoices"},
	}
	idx := Build(docs)
	links := idx.Links["modules/auth.md"]
	if len(links) != 1 || links[0] != "modules/session.md" {
		t.Fatalf("expected auth.md to link to session.md (it mentions 'session'), got %v", links)
	}
	if len(idx.Links["modules/billing.md"]) != 0 {
		t.Fatalf("billing.md mentions no other modules, expected no links, got %v", idx.Links["modules/billing.md"])
	}
}

func TestBuildLinksIgnoresSelfReference(t *testing.T) {
	docs := []Doc{
		{Path: "modules/auth.md", Content: "the auth module handles login"},
	}
	idx := Build(docs)
	if len(idx.Links["modules/auth.md"]) != 0 {
		t.Fatalf("a doc mentioning its own stem should not link to itself, got %v", idx.Links["modules/auth.md"])
	}
}

func TestSearchExpandedIncludesLinkedDocs(t *testing.T) {
	docs := []Doc{
		{Path: "modules/auth.md", Content: "handles authentication, delegates token storage to session"},
		{Path: "modules/session.md", Content: "stores session tokens"},
		{Path: "modules/billing.md", Content: "calculates a monthly invoice total from line items"},
	}
	idx := Build(docs)

	matches := idx.SearchExpanded("authentication login", 1)
	paths := make(map[string]string)
	for _, m := range matches {
		paths[m.Path] = m.Via
	}
	if _, ok := paths["modules/auth.md"]; !ok {
		t.Fatal("expected auth.md in results as a direct match")
	}
	if via, ok := paths["modules/session.md"]; !ok {
		t.Fatal("expected session.md in results as a link expansion from auth.md")
	} else if via != "modules/auth.md" {
		t.Errorf("Via = %q, want modules/auth.md", via)
	}
	if _, ok := paths["modules/billing.md"]; ok {
		t.Error("billing.md should not appear — it is unrelated and not linked from auth.md")
	}
}

func TestSearchExpandedNoDuplicates(t *testing.T) {
	docs := []Doc{
		{Path: "modules/auth.md", Content: "handles authentication, delegates storage to session"},
		{Path: "modules/session.md", Content: "stores session tokens, calls auth for validation"},
	}
	idx := Build(docs)

	matches := idx.SearchExpanded("authentication session", 2)
	seen := make(map[string]int)
	for _, m := range matches {
		seen[m.Path]++
	}
	for path, count := range seen {
		if count > 1 {
			t.Errorf("%s appears %d times — link expansion must not duplicate direct results", path, count)
		}
	}
}

func TestSearchRanksSimilarDocAboveUnrelatedDoc(t *testing.T) {
	docs := []Doc{
		{Path: "modules/phone.md", Content: "validates a phone number, checks format and area code, returns an error for invalid input"},
		{Path: "modules/email.md", Content: "validates an email address, checks the domain part, returns an error for invalid input"},
		{Path: "modules/billing.md", Content: "calculates a monthly invoice total from line items and applies tax rates"},
	}
	idx := Build(docs)

	matches := idx.Search("check that a phone number is formatted correctly", 3)
	if len(matches) == 0 {
		t.Fatal("expected at least one match")
	}
	if matches[0].Path != "modules/phone.md" {
		t.Fatalf("top match = %q, want modules/phone.md (the actual similar doc), matches=%+v", matches[0].Path, matches)
	}

	// The unrelated billing doc should score below both validators.
	var billingScore, phoneScore float64
	for _, m := range matches {
		if m.Path == "modules/billing.md" {
			billingScore = m.Score
		}
		if m.Path == "modules/phone.md" {
			phoneScore = m.Score
		}
	}
	if billingScore >= phoneScore {
		t.Errorf("unrelated doc (billing, score %v) should score below the actual match (phone, score %v)", billingScore, phoneScore)
	}
}

// TestSearchFindsNearDuplicateWithDifferentWording is the actual dedup
// use case this feature exists for: two independently-written functions
// doing the same thing under different names, not two obviously
// different topics.
func TestSearchFindsNearDuplicateWithDifferentWording(t *testing.T) {
	docs := []Doc{
		{Path: "modules/validate_us_phone.md", Content: "ValidateUSPhone checks a phone string matches NANP format and returns an error if not"},
		{Path: "modules/check_phone_number.md", Content: "CheckPhoneNumber verifies a phone string matches NANP format and returns an error if not"},
		{Path: "modules/billing.md", Content: "calculates a monthly invoice total from line items and applies tax rates"},
	}
	idx := Build(docs)

	matches := idx.Search("I need to validate a US phone number matches NANP format", 3)
	scores := make(map[string]float64)
	for _, m := range matches {
		scores[m.Path] = m.Score
	}

	for _, path := range []string{"modules/validate_us_phone.md", "modules/check_phone_number.md"} {
		if scores[path] <= scores["modules/billing.md"] {
			t.Errorf("near-duplicate doc %s (score %v) should outscore the unrelated doc (score %v) — this is the case a developer relies on to find reusable code", path, scores[path], scores["modules/billing.md"])
		}
	}
}

func TestSearchReturnsEmptyForEmptyIndex(t *testing.T) {
	idx := Build(nil)
	matches := idx.Search("anything", 5)
	if len(matches) != 0 {
		t.Errorf("expected no matches from an empty index, got %+v", matches)
	}
}

func TestSaveAndLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "vectors.db")

	docs := []Doc{{Path: "modules/phone.md", Content: "validates a phone number"}}
	idx := Build(docs)
	if err := SaveToFile(idx, path); err != nil {
		t.Fatalf("SaveToFile: %v", err)
	}

	loaded, err := LoadFromFile(path)
	if err != nil {
		t.Fatalf("LoadFromFile: %v", err)
	}
	matches := loaded.Search("phone number validation", 1)
	if len(matches) != 1 || matches[0].Path != "modules/phone.md" {
		t.Fatalf("loaded index search failed, got %+v", matches)
	}
}

func TestLoadFromFileMissingReturnsError(t *testing.T) {
	_, err := LoadFromFile(filepath.Join(os.TempDir(), "does-not-exist-vectors.db"))
	if err == nil {
		t.Fatal("expected an error loading a missing index file")
	}
}

func TestEnsureIndexBuildsWhenMissing(t *testing.T) {
	dir := t.TempDir()
	wikiDir := filepath.Join(dir, "wiki")
	os.MkdirAll(wikiDir, 0o755)
	os.WriteFile(filepath.Join(wikiDir, "a.md"), []byte("validates a phone number"), 0o644)

	idx, err := EnsureIndex(wikiDir, filepath.Join(dir, "index", "vectors.db"))
	if err != nil {
		t.Fatalf("EnsureIndex: %v", err)
	}
	if len(idx.Docs) != 1 {
		t.Fatalf("got %d docs, want 1", len(idx.Docs))
	}
}

func TestEnsureIndexRebuildsWhenWikiContentChanges(t *testing.T) {
	dir := t.TempDir()
	wikiDir := filepath.Join(dir, "wiki")
	os.MkdirAll(wikiDir, 0o755)
	path := filepath.Join(wikiDir, "a.md")
	os.WriteFile(path, []byte("original content"), 0o644)
	indexPath := filepath.Join(dir, "index", "vectors.db")

	if _, err := EnsureIndex(wikiDir, indexPath); err != nil {
		t.Fatalf("EnsureIndex (first): %v", err)
	}

	os.WriteFile(path, []byte("changed content"), 0o644)
	idx, err := EnsureIndex(wikiDir, indexPath)
	if err != nil {
		t.Fatalf("EnsureIndex (second): %v", err)
	}
	if idx.Docs[0].Content != "changed content" {
		t.Fatalf("index was not rebuilt after wiki content changed, got %q", idx.Docs[0].Content)
	}
}

func TestEnsureIndexReusesFreshIndex(t *testing.T) {
	dir := t.TempDir()
	wikiDir := filepath.Join(dir, "wiki")
	os.MkdirAll(wikiDir, 0o755)
	os.WriteFile(filepath.Join(wikiDir, "a.md"), []byte("stable content"), 0o644)
	indexPath := filepath.Join(dir, "index", "vectors.db")

	if _, err := EnsureIndex(wikiDir, indexPath); err != nil {
		t.Fatalf("EnsureIndex (first): %v", err)
	}
	info1, err := os.Stat(indexPath)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}

	if _, err := EnsureIndex(wikiDir, indexPath); err != nil {
		t.Fatalf("EnsureIndex (second): %v", err)
	}
	info2, err := os.Stat(indexPath)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if !info1.ModTime().Equal(info2.ModTime()) {
		t.Error("EnsureIndex rewrote an unchanged index — staleness check should have skipped the rebuild")
	}
}
