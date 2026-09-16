package ui_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/leonp92/golem/internal/orchestrator/auth"
	"github.com/leonp92/golem/internal/orchestrator/db"
	"github.com/leonp92/golem/internal/orchestrator/ui"
)

// TestGitHubSettingsPage_EscapesUserSuppliedValues guards RISK AREA 3:
// Label is user-supplied and LastError carries raw GitHub API error text, so
// both must go through html/template's normal escaping — never
// template.HTML — or a crafted label/error becomes stored XSS on this page.
func TestGitHubSettingsPage_EscapesUserSuppliedValues(t *testing.T) {
	gdb, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	repo := db.GitHubRepo{RepoRemote: "https://github.com/org/repo",
		Owner: "org", Name: "repo", Enabled: true,
		Label:     `<script>alert("label")</script>`,
		LastError: `<img src=x onerror=alert('err')>`,
	}
	if err := gdb.Create(&repo).Error; err != nil {
		t.Fatalf("seed repo: %v", err)
	}

	user := db.User{Username: "leon", PasswordHash: "x"}
	gdb.Create(&user)
	w := httptest.NewRecorder()
	if err := auth.CreateSession(gdb, w, user.ID, false); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	cookie := w.Result().Cookies()[0]

	tmpls, err := ui.LoadTemplates()
	if err != nil {
		t.Fatalf("LoadTemplates: %v", err)
	}
	h := ui.NewHandlersWithMap(gdb, tmpls, false)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	req := httptest.NewRequest(http.MethodGet, "/settings/github", nil)
	withSession(req, cookie)
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if strings.Contains(body, "<script>alert") {
		t.Error("Label was rendered unescaped — stored XSS")
	}
	if strings.Contains(body, "<img src=x onerror") {
		t.Error("LastError was rendered unescaped — stored XSS")
	}
	if !strings.Contains(body, "&lt;script&gt;") {
		t.Error("expected Label to appear HTML-escaped")
	}
	if !strings.Contains(body, "&lt;img") {
		t.Error("expected LastError to appear HTML-escaped")
	}
}

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

func TestGitHubSettingsPageListsRepos(t *testing.T) {
	gdb, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	repo := db.GitHubRepo{RepoRemote: "https://github.com/org/repo",
		Owner: "org", Name: "repo", Enabled: true, Label: "golem"}
	if err := gdb.Create(&repo).Error; err != nil {
		t.Fatalf("seed repo: %v", err)
	}

	user := db.User{Username: "leon", PasswordHash: "x"}
	gdb.Create(&user)
	w := httptest.NewRecorder()
	if err := auth.CreateSession(gdb, w, user.ID, false); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	cookie := w.Result().Cookies()[0]

	tmpls, err := ui.LoadTemplates()
	if err != nil {
		t.Fatalf("LoadTemplates: %v", err)
	}
	h := ui.NewHandlersWithMap(gdb, tmpls, false)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	req := httptest.NewRequest(http.MethodGet, "/settings/github", nil)
	withSession(req, cookie)
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	for _, want := range []string{"org/repo", "golem", "Sync now"} {
		if !strings.Contains(body, want) {
			t.Errorf("page missing %q", want)
		}
	}
}

func TestGitHubSettingsPage_RequiresSession(t *testing.T) {
	gdb, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}

	h := ui.NewHandlers(gdb, nil)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	req := httptest.NewRequest(http.MethodGet, "/settings/github", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusFound {
		t.Errorf("expected redirect, got %d", w.Code)
	}
	if w.Header().Get("Location") != "/login" {
		t.Errorf("expected /login redirect, got %q", w.Header().Get("Location"))
	}
}

// TestGitHubSettingsPage_PlaceholderRepoHasNoSyncButton guards RISK AREA 4:
// a repo a shem has registered but that has no db.GitHubRepo settings row
// yet must not render a "Sync now" button wired to id 0 — that endpoint
// 404s, and would leave the button stuck disabled.
func TestGitHubSettingsPage_PlaceholderRepoHasNoSyncButton(t *testing.T) {
	gdb, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	shem := db.Shem{
		Name:       "shem1",
		APIKeyHash: "x",
		Repos:      `["https://github.com/org/registered","https://github.com/org/unregistered"]`,
		Status:     "offline",
	}
	if err := gdb.Create(&shem).Error; err != nil {
		t.Fatalf("seed shem: %v", err)
	}
	registered := db.GitHubRepo{RepoRemote: "https://github.com/org/registered",
		Owner: "org", Name: "registered", Enabled: true, Label: "golem"}
	if err := gdb.Create(&registered).Error; err != nil {
		t.Fatalf("seed repo: %v", err)
	}

	user := db.User{Username: "leon", PasswordHash: "x"}
	gdb.Create(&user)
	w := httptest.NewRecorder()
	if err := auth.CreateSession(gdb, w, user.ID, false); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	cookie := w.Result().Cookies()[0]

	tmpls, err := ui.LoadTemplates()
	if err != nil {
		t.Fatalf("LoadTemplates: %v", err)
	}
	h := ui.NewHandlersWithMap(gdb, tmpls, false)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	req := httptest.NewRequest(http.MethodGet, "/settings/github", nil)
	withSession(req, cookie)
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !strings.Contains(body, "org/unregistered") {
		t.Error("expected the shem-registered-but-unsaved repo to be listed")
	}
	if strings.Contains(body, `data-repo-id="0"`) {
		t.Error("placeholder repo (no settings row) must not offer a sync button wired to id 0")
	}
}

func TestGitHubSettingsSubmit(t *testing.T) {
	cases := []struct {
		name        string
		seed        *db.GitHubRepo
		remote      string
		formBody    string
		wantEnabled bool
		wantLabel   string
		wantOwner   string
		wantName    string
	}{
		{
			name:        "creates a new row when none exists",
			remote:      "https://github.com/org/new",
			formBody:    "repo_remote=https://github.com/org/new&enabled=on&label=custom",
			wantEnabled: true,
			wantLabel:   "custom",
			wantOwner:   "org",
			wantName:    "new",
		},
		{
			name: "disables an existing row when the checkbox is absent",
			seed: &db.GitHubRepo{RepoRemote: "https://github.com/org/existing",
				Owner: "org", Name: "existing", Enabled: true, Label: "golem"},
			remote:      "https://github.com/org/existing",
			formBody:    "repo_remote=https://github.com/org/existing&label=golem",
			wantEnabled: false,
			wantLabel:   "golem",
		},
		{
			name: "updates the label on an existing row",
			seed: &db.GitHubRepo{RepoRemote: "https://github.com/org/relabel",
				Owner: "org", Name: "relabel", Enabled: true, Label: "golem"},
			remote:      "https://github.com/org/relabel",
			formBody:    "repo_remote=https://github.com/org/relabel&enabled=on&label=renamed",
			wantEnabled: true,
			wantLabel:   "renamed",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gdb, err := db.Open(":memory:")
			if err != nil {
				t.Fatalf("db.Open: %v", err)
			}
			if tc.seed != nil {
				if err := gdb.Create(tc.seed).Error; err != nil {
					t.Fatalf("seed: %v", err)
				}
			}
			user := db.User{Username: "leon", PasswordHash: "x"}
			gdb.Create(&user)
			w := httptest.NewRecorder()
			if err := auth.CreateSession(gdb, w, user.ID, false); err != nil {
				t.Fatalf("CreateSession: %v", err)
			}
			cookie := w.Result().Cookies()[0]

			h := ui.NewHandlers(gdb, nil)
			mux := http.NewServeMux()
			h.RegisterRoutes(mux)

			req := httptest.NewRequest(http.MethodPost, "/settings/github", strings.NewReader(tc.formBody))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			withSession(req, cookie)
			w = httptest.NewRecorder()
			mux.ServeHTTP(w, req)

			if w.Code != http.StatusSeeOther {
				t.Fatalf("status = %d, want 303: %s", w.Code, w.Body.String())
			}
			if loc := w.Header().Get("Location"); loc != "/settings/github" {
				t.Errorf("Location = %q, want /settings/github", loc)
			}

			var repo db.GitHubRepo
			if err := gdb.Where("repo_remote = ?", tc.remote).First(&repo).Error; err != nil {
				t.Fatalf("expected a row for %s: %v", tc.remote, err)
			}
			if repo.Enabled != tc.wantEnabled {
				t.Errorf("Enabled = %v, want %v", repo.Enabled, tc.wantEnabled)
			}
			if repo.Label != tc.wantLabel {
				t.Errorf("Label = %q, want %q", repo.Label, tc.wantLabel)
			}
			if tc.wantOwner != "" && repo.Owner != tc.wantOwner {
				t.Errorf("Owner = %q, want %q", repo.Owner, tc.wantOwner)
			}
			if tc.wantName != "" && repo.Name != tc.wantName {
				t.Errorf("Name = %q, want %q", repo.Name, tc.wantName)
			}
		})
	}
}

func TestTicketDetailShowsIssueLinkAndReadOnlyTitle(t *testing.T) {
	gdb, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	n := 7
	ticket := db.Ticket{ID: "gh-issue-001", RepoRemote: "https://github.com/org/repo",
		Title: "Add rate limiting", Branch: "ticket/x-t1", Description: "d",
		Phase: "implement", IssueNumber: &n,
		IssueURL: "https://github.com/org/repo/issues/7"}
	if err := gdb.Create(&ticket).Error; err != nil {
		t.Fatalf("seed ticket: %v", err)
	}

	user := db.User{Username: "leon", PasswordHash: "x"}
	gdb.Create(&user)
	w := httptest.NewRecorder()
	if err := auth.CreateSession(gdb, w, user.ID, false); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	cookie := w.Result().Cookies()[0]

	tmpls, err := ui.LoadTemplates()
	if err != nil {
		t.Fatalf("LoadTemplates: %v", err)
	}
	h := ui.NewHandlersWithMap(gdb, tmpls, false)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	req := httptest.NewRequest(http.MethodGet, "/tickets/gh-issue-001", nil)
	withSession(req, cookie)
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !strings.Contains(body, "https://github.com/org/repo/issues/7") {
		t.Error("issue URL not rendered")
	}
	if !strings.Contains(body, "synced from GitHub") {
		t.Error("read-only provenance note missing — an editable title would silently revert on the next ingest")
	}
}

// TestTicketDetailDescriptionIsEscapedPlainText guards against stored XSS via
// .Ticket.Description. Description is populated from a GitHub issue body —
// content anyone who can open or label an issue in a synced repo controls.
// layout.html's shared renderMarkdown() helper re-parses every .md-content
// element's textContent with marked.js (no sanitizer: marked dropped its
// `sanitize` option years ago) and assigns the result to innerHTML. That
// combination — decode via textContent, re-parse raw HTML, assign via
// innerHTML — executes any markup the field contains, regardless of the Go
// template layer's escaping. Description must be rendered as escaped plain
// text and must never carry the md-content class.
func TestTicketDetailDescriptionIsEscapedPlainText(t *testing.T) {
	gdb, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	n := 9
	const payload = `<img src=x onerror=alert(1)>`
	ticket := db.Ticket{ID: "gh-issue-004", RepoRemote: "https://github.com/org/repo",
		Title: "XSS check", Branch: "ticket/x-t4", Description: payload,
		Phase: "implement", IssueNumber: &n,
		IssueURL: "https://github.com/org/repo/issues/9"}
	if err := gdb.Create(&ticket).Error; err != nil {
		t.Fatalf("seed ticket: %v", err)
	}

	user := db.User{Username: "leon", PasswordHash: "x"}
	gdb.Create(&user)
	w := httptest.NewRecorder()
	if err := auth.CreateSession(gdb, w, user.ID, false); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	cookie := w.Result().Cookies()[0]

	tmpls, err := ui.LoadTemplates()
	if err != nil {
		t.Fatalf("LoadTemplates: %v", err)
	}
	h := ui.NewHandlersWithMap(gdb, tmpls, false)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	req := httptest.NewRequest(http.MethodGet, "/tickets/gh-issue-004", nil)
	withSession(req, cookie)
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
	}
	body := w.Body.String()

	if strings.Contains(body, payload) {
		t.Fatal("Description was rendered as raw HTML — stored XSS")
	}
	escaped := "&lt;img src=x onerror=alert(1)&gt;"
	payloadIdx := strings.Index(body, escaped)
	if payloadIdx == -1 {
		t.Fatal("expected Description to appear HTML-escaped")
	}

	// The escaped payload must not sit inside an element carrying the
	// md-content class — that class is what triggers the client-side
	// marked.js/innerHTML re-parse that would undo this escaping. Description
	// is the sole text node of its wrapping element, so the payload appears
	// immediately after that element's opening tag; walk back to it.
	tagStart := strings.LastIndex(body[:payloadIdx], "<")
	if tagStart == -1 {
		t.Fatal("could not locate the element wrapping Description")
	}
	tagEndRel := strings.Index(body[tagStart:], ">")
	if tagEndRel == -1 {
		t.Fatal("could not locate the end of the element wrapping Description")
	}
	openTag := body[tagStart : tagStart+tagEndRel+1]
	if strings.Contains(openTag, "md-content") {
		t.Errorf("Description's element must not carry md-content (got %q) — "+
			"it would be re-parsed as HTML via marked.js/innerHTML, undoing "+
			"the escaping asserted above and executing the payload", openTag)
	}
}

// TestTicketDetailNonLinkedTicketUnchanged guards against regressing the
// non-GitHub-linked ticket rendering path: it must show no GitHub
// provenance note, since .Ticket.IssueNumber is nil.
func TestTicketDetailNonLinkedTicketUnchanged(t *testing.T) {
	gdb, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	ticket := db.Ticket{ID: "gh-issue-002", RepoRemote: "https://github.com/org/repo",
		Title: "Plain ticket", Branch: "ticket/x-t2", Description: "d",
		Phase: "implement"}
	if err := gdb.Create(&ticket).Error; err != nil {
		t.Fatalf("seed ticket: %v", err)
	}

	user := db.User{Username: "leon", PasswordHash: "x"}
	gdb.Create(&user)
	w := httptest.NewRecorder()
	if err := auth.CreateSession(gdb, w, user.ID, false); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	cookie := w.Result().Cookies()[0]

	tmpls, err := ui.LoadTemplates()
	if err != nil {
		t.Fatalf("LoadTemplates: %v", err)
	}
	h := ui.NewHandlersWithMap(gdb, tmpls, false)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	req := httptest.NewRequest(http.MethodGet, "/tickets/gh-issue-002", nil)
	withSession(req, cookie)
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if strings.Contains(body, "synced from GitHub") {
		t.Error("non-linked ticket must not show a GitHub provenance note")
	}
}

// TestTicketDetailOffersStartControlOnlyForPendingApproval guards the intake
// approval gate's UI surface (spec Amendment 1): a pending-approval,
// GitHub-linked ticket must offer the human "Approve & Start" control that
// posts action=start, and no other phase must offer it — including
// unassigned, which is what a released pending-approval ticket becomes.
func TestTicketDetailOffersStartControlOnlyForPendingApproval(t *testing.T) {
	someShem := uint(7)
	tests := []struct {
		name           string
		phase          string
		intakeApproved bool
		assignedShem   *uint
		wantStart      bool
	}{
		{name: "pending-approval, unapproved: offers it",
			phase: "pending-approval", intakeApproved: false, wantStart: true},
		{name: "unassigned, approved (properly released, awaiting claim): does not offer it",
			phase: "unassigned", intakeApproved: true, wantStart: false},
		// Fix round 4: the card is rendered on provenance+approval state, not
		// on Phase == "pending-approval", so a ticket stranded outside that
		// phase (e.g. by close/needs-attention/requeue) while still
		// unapproved must still offer the recovery control here.
		{name: "unassigned, unapproved (stranded, recovery state): offers it",
			phase: "unassigned", intakeApproved: false, wantStart: true},
		{name: "implement, approved (actively running): does not offer it",
			phase: "implement", intakeApproved: true, wantStart: false},
		// Fix round 5: a claimed-but-unapproved ticket (a state that should
		// never legitimately arise, but round 4's card condition did not
		// defend against it) must not offer "start" either — starting it
		// would double-claim it, same as actionStart's own guard.
		{name: "implement, unapproved, claimed: does not offer it (double-claim guard)",
			phase: "implement", intakeApproved: false, assignedShem: &someShem, wantStart: false},
		{name: "closed, unapproved: does not offer it even though unapproved",
			phase: "closed", intakeApproved: false, wantStart: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gdb, err := db.Open(":memory:")
			if err != nil {
				t.Fatalf("db.Open: %v", err)
			}
			n := 11
			id := fmt.Sprintf("gh-issue-start-%s-%v-claimed-%v", tt.phase, tt.intakeApproved, tt.assignedShem != nil)
			ticket := db.Ticket{ID: id, RepoRemote: "https://github.com/org/repo",
				Title: "t", Branch: "ticket/x", Description: "d",
				Phase: tt.phase, IssueNumber: &n, IntakeApproved: tt.intakeApproved,
				AssignedShem: tt.assignedShem,
				IssueURL:     "https://github.com/org/repo/issues/11"}
			if err := gdb.Create(&ticket).Error; err != nil {
				t.Fatalf("seed ticket: %v", err)
			}

			user := db.User{Username: "leon", PasswordHash: "x"}
			gdb.Create(&user)
			w := httptest.NewRecorder()
			if err := auth.CreateSession(gdb, w, user.ID, false); err != nil {
				t.Fatalf("CreateSession: %v", err)
			}
			cookie := w.Result().Cookies()[0]

			tmpls, err := ui.LoadTemplates()
			if err != nil {
				t.Fatalf("LoadTemplates: %v", err)
			}
			h := ui.NewHandlersWithMap(gdb, tmpls, false)
			mux := http.NewServeMux()
			h.RegisterRoutes(mux)

			req := httptest.NewRequest(http.MethodGet, "/tickets/"+ticket.ID, nil)
			withSession(req, cookie)
			w = httptest.NewRecorder()
			mux.ServeHTTP(w, req)

			if w.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
			}
			body := w.Body.String()
			hasStart := strings.Contains(body, `"action":"start"`)
			if hasStart != tt.wantStart {
				t.Errorf("start control present = %v, want %v", hasStart, tt.wantStart)
			}
		})
	}
}

// TestTicketDetailSuppressesRequeueOnlyForPendingApproval guards fix-round-1
// of the intake approval gate (spec Amendment 1): the generic "Re-queue"
// control is a second, unlabeled door that would release a pending-approval
// ticket without the human review "Approve & Start" performs, so it must be
// hidden for that phase. The Close control must still be offered — closing
// an unwanted ingested ticket is legitimate. A phase where re-queue is still
// valid (implement) must still show it, so the suppression can't silently
// regress into hiding the control everywhere.
func TestTicketDetailSuppressesRequeueOnlyForPendingApproval(t *testing.T) {
	tests := []struct {
		name        string
		phase       string
		wantRequeue bool
		wantClose   bool
	}{
		{name: "pending-approval hides requeue, keeps close", phase: "pending-approval", wantRequeue: false, wantClose: true},
		{name: "implement still offers requeue and close", phase: "implement", wantRequeue: true, wantClose: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gdb, err := db.Open(":memory:")
			if err != nil {
				t.Fatalf("db.Open: %v", err)
			}
			n := 12
			ticket := db.Ticket{ID: "gh-issue-requeue-" + tt.phase, RepoRemote: "https://github.com/org/repo",
				Title: "t", Branch: "ticket/x", Description: "d",
				Phase: tt.phase, IssueNumber: &n,
				IssueURL: "https://github.com/org/repo/issues/12"}
			if err := gdb.Create(&ticket).Error; err != nil {
				t.Fatalf("seed ticket: %v", err)
			}

			user := db.User{Username: "leon", PasswordHash: "x"}
			gdb.Create(&user)
			w := httptest.NewRecorder()
			if err := auth.CreateSession(gdb, w, user.ID, false); err != nil {
				t.Fatalf("CreateSession: %v", err)
			}
			cookie := w.Result().Cookies()[0]

			tmpls, err := ui.LoadTemplates()
			if err != nil {
				t.Fatalf("LoadTemplates: %v", err)
			}
			h := ui.NewHandlersWithMap(gdb, tmpls, false)
			mux := http.NewServeMux()
			h.RegisterRoutes(mux)

			req := httptest.NewRequest(http.MethodGet, "/tickets/"+ticket.ID, nil)
			withSession(req, cookie)
			w = httptest.NewRecorder()
			mux.ServeHTTP(w, req)

			if w.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
			}
			body := w.Body.String()
			hasRequeue := strings.Contains(body, `"action":"requeue"`)
			if hasRequeue != tt.wantRequeue {
				t.Errorf("requeue control present = %v, want %v", hasRequeue, tt.wantRequeue)
			}
			hasClose := strings.Contains(body, `"action":"close"`)
			if hasClose != tt.wantClose {
				t.Errorf("close control present = %v, want %v", hasClose, tt.wantClose)
			}
		})
	}
}

// TestDashboardShowsIssueBadgeForLinkedTicket guards the ticket_row partial:
// its dot context is ui.TicketRow (with a nested .Ticket), so the badge must
// reference .Ticket.IssueNumber/.Ticket.IssueURL, not a bare .IssueNumber —
// the latter would fail template execution for every ticket on the
// dashboard, not just the linked one.
func TestDashboardShowsIssueBadgeForLinkedTicket(t *testing.T) {
	gdb, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	n := 42
	ticket := db.Ticket{ID: "gh-issue-003", RepoRemote: "https://github.com/org/repo",
		Title: "Linked", Branch: "ticket/x-t3", Description: "d",
		Phase: "unassigned", IssueNumber: &n,
		IssueURL: "https://github.com/org/repo/issues/42"}
	if err := gdb.Create(&ticket).Error; err != nil {
		t.Fatalf("seed ticket: %v", err)
	}

	user := db.User{Username: "leon", PasswordHash: "x"}
	gdb.Create(&user)
	w := httptest.NewRecorder()
	if err := auth.CreateSession(gdb, w, user.ID, false); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	cookie := w.Result().Cookies()[0]

	tmpls, err := ui.LoadTemplates()
	if err != nil {
		t.Fatalf("LoadTemplates: %v", err)
	}
	h := ui.NewHandlersWithMap(gdb, tmpls, false)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	req := httptest.NewRequest(http.MethodGet, "/dashboard", nil)
	withSession(req, cookie)
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !strings.Contains(body, `href="https://github.com/org/repo/issues/42"`) {
		t.Error("expected issue badge link on dashboard row")
	}
	if !strings.Contains(body, "#42") {
		t.Error("expected issue number on dashboard row")
	}
}

// dompurifyTagRE captures the whole <script> tag that loads DOMPurify.
var dompurifyTagRE = regexp.MustCompile(`(?i)<script[^>]*purify\.min\.js[^>]*>`)

// TestDOMPurifyIsPinnedAndIntegrityChecked covers re-review finding F5.
//
// Everything above this test asserts that the sanitizer is configured
// correctly. None of it asserts anything about the bytes that configuration is
// handed to. DOMPurify arrives over the network from a public CDN, and it was
// requested as `dompurify@3` with no integrity attribute — a floating major.
// A 3.x release that changes what FORBID_TAGS or ALLOW_DATA_ATTR mean, or a
// compromise of the registry or the CDN, changes the security posture of the
// dashboard with nothing in this suite able to notice.
//
// It is not one dependency among several: it is the control. The markdown
// sink takes an issue body a stranger wrote, runs it through a parser that
// does no sanitizing at all, and assigns the result to innerHTML. DOMPurify
// is the only thing between those two facts.
//
// Pinning an exact version plus a Subresource Integrity hash converts the
// implementers' documented residual — "would not catch a future DOMPurify
// release changing what the config means" — from unbounded into a dependency
// bump that shows up in a diff. The browser refuses to execute bytes that do
// not match the hash, so a swapped file fails closed: DOMPurify is absent,
// renderMarkdown's guard takes the textContent path, and the sink renders as
// plain text rather than unsanitized HTML.
func TestDOMPurifyIsPinnedAndIntegrityChecked(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("templates", "layout.html"))
	if err != nil {
		t.Fatalf("read layout.html: %v", err)
	}
	tag := dompurifyTagRE.FindString(string(src))
	if tag == "" {
		t.Fatal("layout.html has no <script> tag loading purify.min.js")
	}

	checks := []struct {
		name string
		re   *regexp.Regexp
		why  string
	}{
		{
			name: "exact version",
			re:   regexp.MustCompile(`dompurify@\d+\.\d+\.\d+/`),
			why: "a floating major (dompurify@3) lets a future release change what the " +
				"sanitizer config means with nothing in CI able to notice",
		},
		{
			name: "integrity",
			re:   regexp.MustCompile(`integrity="sha(256|384|512)-[A-Za-z0-9+/]+={0,2}"`),
			why:  "without it a CDN or registry compromise silently replaces the control",
		},
		{
			name: "crossorigin",
			re:   regexp.MustCompile(`crossorigin="anonymous"`),
			why:  "a cross-origin subresource without it is not integrity-checked at all",
		},
	}
	for _, c := range checks {
		if !c.re.MatchString(tag) {
			t.Errorf("the DOMPurify script tag has no %s — %s\ntag: %s", c.name, c.why, tag)
		}
	}
}
