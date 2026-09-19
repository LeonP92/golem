package ui_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The markdown sink is the one place in the dashboard where text a stranger
// wrote becomes DOM. renderMarkdown in layout.html reads el.textContent —
// which decodes html/template's escaping — runs it through marked, which does
// no sanitizing at all, and assigns the result to innerHTML. Everything in
// this file is about the single control standing between those facts: what it
// is configured to forbid, and which bytes the browser is allowed to run it
// from.

// TestMarkdownSinkIsSanitized guards the client-side markdown pipeline.
// renderMarkdown reads el.textContent — which DECODES html/template's escaping
// — and assigns the result of marked.parse to innerHTML. marked does not
// sanitize. Without DOMPurify, any field routed through .md-content is an XSS
// sink, including .Spec.Message and .Plan.Message, which derive from a GitHub
// issue body by way of an LLM prompt.
func TestMarkdownSinkIsSanitized(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("templates", "layout.html"))
	if err != nil {
		t.Fatalf("read layout.html: %v", err)
	}
	body := string(src)

	if !strings.Contains(body, "dompurify") && !strings.Contains(body, "purify.min.js") {
		t.Error("layout.html does not load DOMPurify")
	}
	if strings.Contains(body, "innerHTML = marked.parse(") {
		t.Error("unsanitized marked.parse output is assigned to innerHTML")
	}
	if !strings.Contains(body, ".sanitize(marked.parse(") {
		t.Error("renderMarkdown does not pass marked's output through the sanitizer")
	}
	// Fail closed: the code must handle DOMPurify being absent.
	if !strings.Contains(body, "typeof DOMPurify") {
		t.Error("renderMarkdown does not guard against DOMPurify being unavailable")
	}

	// Finding S2: the guard read `typeof DOMPurify === 'object'`, but the
	// UMD build the CDN tag above serves exports a FUNCTION. The guard was
	// therefore always false and every .md-content element silently took the
	// textContent fallback — markdown rendering was broken everywhere and
	// Task 18's sanitizer mitigation had never executed in a browser.
	// Verified in jsdom against the real CDN bytes of dompurify@3 (3.4.15):
	// typeof DOMPurify === "function", guard false.
	if strings.Contains(body, "typeof DOMPurify === 'object'") {
		t.Error("the sanitizer guard tests only for 'object'; DOMPurify's UMD build is a function, " +
			"so this guard is always false and the sanitizer never runs (finding S2)")
	}
	if !strings.Contains(body, "typeof purifier === 'function'") {
		t.Error("the sanitizer guard does not accept a function-valued DOMPurify namespace")
	}

	// Finding S1: DOMPurify's DEFAULTS preserve <form action>, <button
	// type=submit> and style=, which is a one-click same-origin intake-gate
	// bypass once the guard above is fixed. The sanitizer must be called
	// with an explicit deny list, not with no configuration at all.
	// Each token is checked inside the list it belongs to, not against the
	// whole config literal (re-review finding F4). 'style' and 'form' appear
	// in BOTH lists and mean different things in each: <style> is a tag that
	// can restyle the page, style= is the attribute that reconstructs S1's
	// invisible full-viewport overlay; <form> is the element, form= is the
	// attribute that re-parents an orphan control into one. A whole-literal
	// substring check is satisfied by either occurrence, so deleting 'style'
	// from FORBID_ATTR — precisely the half that kills the overlay — used to
	// pass this test unchanged.
	forbidden := []struct {
		list  string
		token string
		why   string
	}{
		{"FORBID_TAGS", "'form'", "DOMPurify's defaults keep <form action>"},
		{"FORBID_TAGS", "'input'", "a form needs controls"},
		{"FORBID_TAGS", "'button'", "<button type=submit> is the one click"},
		{"FORBID_TAGS", "'select'", "a control that carries a value"},
		{"FORBID_TAGS", "'textarea'", "a control that carries a value"},
		{"FORBID_TAGS", "'option'", "a control that carries a value"},
		{"FORBID_TAGS", "'label'", "a label makes a hidden control clickable"},
		{"FORBID_TAGS", "'fieldset'", "groups controls for one submit"},
		{"FORBID_TAGS", "'style'", "a <style> block can restyle the whole page"},
		{"FORBID_ATTR", "'style'", "reconstructs S1's invisible full-viewport overlay without a <style> tag"},
		{"FORBID_ATTR", "'action'", "where a resurrected form would post"},
		{"FORBID_ATTR", "'formaction'", "overrides the action from the submit control"},
		{"FORBID_ATTR", "'form'", "re-parents an orphan control into a form elsewhere on the page"},
	}
	cfgStart := strings.Index(body, "var MD_SANITIZE_CONFIG")
	if cfgStart == -1 {
		t.Fatal("renderMarkdown passes no explicit sanitizer configuration (finding S1)")
	}
	cfgEnd := strings.Index(body[cfgStart:], "};")
	if cfgEnd == -1 {
		t.Fatal("could not locate the end of the sanitizer configuration literal")
	}
	cfg := body[cfgStart : cfgStart+cfgEnd]
	lists := map[string]string{}
	for _, name := range []string{"FORBID_TAGS", "FORBID_ATTR"} {
		marker := name + ": ["
		start := strings.Index(cfg, marker)
		if start == -1 {
			t.Fatalf("sanitizer config has no %s list", name)
		}
		rest := cfg[start+len(marker):]
		end := strings.Index(rest, "]")
		if end == -1 {
			t.Fatalf("sanitizer config's %s list is not terminated", name)
		}
		lists[name] = rest[:end]
	}
	for _, f := range forbidden {
		if !strings.Contains(lists[f.list], f.token) {
			t.Errorf("sanitizer config's %s does not contain %s — %s\n%s is [%s]",
				f.list, f.token, f.why, f.list, lists[f.list])
		}
	}
	// data-hx-* is what ALLOW_DATA_ATTR covers: FORBID_ATTR matches exact
	// names, never patterns, and DOMPurify permits every data-* attribute by
	// default.
	if !strings.Contains(cfg, "ALLOW_DATA_ATTR: false") {
		t.Error("sanitizer config leaves data-* attributes allowed, so data-hx-* survives")
	}
	if !strings.Contains(body, "MD_SANITIZE_CONFIG)") {
		t.Error("the sanitizer configuration is declared but never passed to sanitize()")
	}

	// Per-element isolation: forEach has no exception isolation, so if
	// marked.parse throws on one pathological .md-content body (marked has
	// known failure modes on deeply nested input), the exception must not
	// escape and abort rendering for every subsequent element in the batch.
	// Scope the check to renderMarkdown's body specifically, not the whole
	// file, so an unrelated try/catch elsewhere can't satisfy it.
	fnStart := strings.Index(body, "function renderMarkdown(root)")
	if fnStart == -1 {
		t.Fatal("could not locate renderMarkdown function body")
	}
	fnEnd := strings.Index(body[fnStart:], "document.addEventListener('DOMContentLoaded'")
	if fnEnd == -1 {
		t.Fatal("could not locate end of renderMarkdown function body")
	}
	fnBody := body[fnStart : fnStart+fnEnd]
	if !strings.Contains(fnBody, "try {") || !strings.Contains(fnBody, "catch") {
		t.Error("renderMarkdown does not isolate a single element's parse/sanitize failure with try/catch")
	}
}

// The pinning and integrity half of this — that the bytes DOMPurify and marked
// arrive as are the bytes we chose — lives in subresources_test.go, as a rule
// about every external subresource on the page rather than about this pair.
