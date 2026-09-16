package ui_test

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/leonp92/golem/internal/orchestrator/auth"
	"github.com/leonp92/golem/internal/orchestrator/db"
	"github.com/leonp92/golem/internal/orchestrator/ui"
)

// withSession attaches a session cookie AND the CSRF token derived from it,
// which is what a real browser sends: ui.Handlers.render puts the token in
// the page and the forms/htmx listener in layout.html put it on the wire.
// Tests that exercise the absence of a token add the cookie directly.
func withSession(req *http.Request, cookie *http.Cookie) *http.Request {
	req.AddCookie(cookie)
	req.Header.Set(auth.CSRFHeader, auth.CSRFTokenForSession(cookie.Value))
	return req
}

// TestPagesPublishCSRFToken proves the token actually reaches the browser on
// every session-authenticated page — in the meta element layout.html's htmx
// listener reads, and, for the pages whose forms are submitted by ordinary
// browser navigation rather than by htmx, as a hidden form field too. A
// server that enforces a token the page never renders is an outage, not a
// control.
func TestPagesPublishCSRFToken(t *testing.T) {
	gdb, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	repo := db.GitHubRepo{RepoRemote: "https://github.com/org/repo",
		Owner: "org", Name: "repo", Enabled: true, Label: "golem"}
	if err := gdb.Create(&repo).Error; err != nil {
		t.Fatalf("seed repo: %v", err)
	}
	ticket := db.Ticket{ID: "csrf-page-1", RepoRemote: "https://github.com/org/repo",
		BaseBranch: "main", Branch: "b", Title: "t", Description: "d", Phase: "unassigned"}
	if err := gdb.Create(&ticket).Error; err != nil {
		t.Fatalf("seed ticket: %v", err)
	}
	user := db.User{Username: "leon", PasswordHash: "x"}
	gdb.Create(&user)

	rec := httptest.NewRecorder()
	if err := auth.CreateSession(gdb, rec, user.ID, false); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	cookie := rec.Result().Cookies()[0]
	token := auth.CSRFTokenForSession(cookie.Value)

	tmpls, err := ui.LoadTemplates()
	if err != nil {
		t.Fatalf("LoadTemplates: %v", err)
	}
	h := ui.NewHandlersWithMap(gdb, tmpls, false)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	tests := []struct {
		path           string
		wantHiddenForm bool
	}{
		{path: "/dashboard"},
		{path: "/shems"},
		{path: "/settings/github", wantHiddenForm: true},
		{path: "/tickets/new", wantHiddenForm: true},
		{path: "/tickets/csrf-page-1"},
	}

	for _, tc := range tests {
		t.Run(tc.path, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tc.path, nil)
			req.AddCookie(cookie)
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, req)
			if w.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
			}
			body := w.Body.String()
			if !strings.Contains(body, `<meta name="csrf-token" content="`+token+`">`) {
				t.Error("page does not publish the session's CSRF token in a meta element")
			}
			if tc.wantHiddenForm &&
				!strings.Contains(body, `name="csrf_token" value="`+token+`"`) {
				t.Error("page has a natively-submitted form with no hidden csrf_token field")
			}
		})
	}
}

// TestGitHubSettingsSubmit_RequiresCSRFToken: /settings/github is
// session-authenticated and state-changing, so it is reachable by the same
// injected same-origin form as the ticket action dispatcher (finding S1).
func TestGitHubSettingsSubmit_RequiresCSRFToken(t *testing.T) {
	const formBody = "repo_remote=https://github.com/org/new&enabled=on&label=evil"

	tests := []struct {
		name     string
		token    func(cookie *http.Cookie) string
		wantCode int
	}{
		{name: "no token", token: func(*http.Cookie) string { return "" }, wantCode: http.StatusForbidden},
		{name: "wrong token", token: func(*http.Cookie) string { return "nope" }, wantCode: http.StatusForbidden},
		{
			name:     "correct token",
			token:    func(c *http.Cookie) string { return auth.CSRFTokenForSession(c.Value) },
			wantCode: http.StatusSeeOther,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			gdb, err := db.Open(":memory:")
			if err != nil {
				t.Fatalf("db.Open: %v", err)
			}
			user := db.User{Username: "leon", PasswordHash: "x"}
			gdb.Create(&user)
			rec := httptest.NewRecorder()
			if err := auth.CreateSession(gdb, rec, user.ID, false); err != nil {
				t.Fatalf("CreateSession: %v", err)
			}
			cookie := rec.Result().Cookies()[0]

			tmpls, err := ui.LoadTemplates()
			if err != nil {
				t.Fatalf("LoadTemplates: %v", err)
			}
			h := ui.NewHandlersWithMap(gdb, tmpls, false)
			mux := http.NewServeMux()
			h.RegisterRoutes(mux)

			req := httptest.NewRequest(http.MethodPost, "/settings/github",
				strings.NewReader(formBody))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			req.AddCookie(cookie)
			if tok := tc.token(cookie); tok != "" {
				req.Header.Set(auth.CSRFHeader, tok)
			}
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, req)

			if w.Code != tc.wantCode {
				t.Errorf("status = %d, want %d: %s", w.Code, tc.wantCode, w.Body.String())
			}
			var count int64
			gdb.Model(&db.GitHubRepo{}).Count(&count)
			wantRows := int64(0)
			if tc.wantCode == http.StatusSeeOther {
				wantRows = 1
			}
			if count != wantRows {
				t.Errorf("github_repos rows = %d, want %d", count, wantRows)
			}
		})
	}
}

// TestGitHubSettingsSubmit_RejectsTriggerLabelInGolemNamespace covers finding
// S7. applyPhaseLabel strips every golem:* label from an issue except the one
// it is applying, so a trigger label inside that namespace is removed on the
// first phase transition and the issue silently leaves its own opt-in filter.
// Verified against the real Drain + applyPhaseLabel at 51d9526:
// trigger="golem:triage" on [golem:triage bug golem:brainstorm] left
// [bug golem:implement]. githubSettingsSubmit validated nothing.
//
// The default "golem" and every near-miss outside the namespace are correct
// today and must keep working, so they are in the same table.
func TestGitHubSettingsSubmit_RejectsTriggerLabelInGolemNamespace(t *testing.T) {
	tests := []struct {
		name       string
		label      string
		wantCode   int
		wantStored string
	}{
		{name: "default label", label: "golem", wantCode: http.StatusSeeOther, wantStored: "golem"},
		{name: "hyphen near-miss", label: "golem-adjacent", wantCode: http.StatusSeeOther, wantStored: "golem-adjacent"},
		{name: "suffix near-miss", label: "golemite", wantCode: http.StatusSeeOther, wantStored: "golemite"},
		{name: "case near-miss", label: "Golem", wantCode: http.StatusSeeOther, wantStored: "Golem"},
		{name: "colon but not a prefix", label: "team:golem", wantCode: http.StatusSeeOther, wantStored: "team:golem"},
		{name: "unrelated label", label: "needs-triage", wantCode: http.StatusSeeOther, wantStored: "needs-triage"},
		{name: "owned namespace", label: "golem:triage", wantCode: http.StatusBadRequest},
		{name: "owned namespace, a real phase", label: "golem:implement", wantCode: http.StatusBadRequest},
		{name: "owned namespace, bare prefix", label: "golem:", wantCode: http.StatusBadRequest},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			gdb, err := db.Open(":memory:")
			if err != nil {
				t.Fatalf("db.Open: %v", err)
			}
			user := db.User{Username: "leon", PasswordHash: "x"}
			gdb.Create(&user)
			rec := httptest.NewRecorder()
			if err := auth.CreateSession(gdb, rec, user.ID, false); err != nil {
				t.Fatalf("CreateSession: %v", err)
			}
			cookie := rec.Result().Cookies()[0]

			tmpls, err := ui.LoadTemplates()
			if err != nil {
				t.Fatalf("LoadTemplates: %v", err)
			}
			h := ui.NewHandlersWithMap(gdb, tmpls, false)
			mux := http.NewServeMux()
			h.RegisterRoutes(mux)

			form := url.Values{}
			form.Set("repo_remote", "https://github.com/org/repo")
			form.Set("enabled", "on")
			form.Set("label", tc.label)
			req := httptest.NewRequest(http.MethodPost, "/settings/github",
				strings.NewReader(form.Encode()))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, withSession(req, cookie))

			if w.Code != tc.wantCode {
				t.Fatalf("status = %d, want %d: %s", w.Code, tc.wantCode, w.Body.String())
			}

			var repos []db.GitHubRepo
			gdb.Find(&repos)
			if tc.wantCode == http.StatusBadRequest {
				if len(repos) != 0 {
					t.Errorf("a rejected label was still persisted: %+v", repos)
				}
				// The operator has to be able to see why.
				if !strings.Contains(w.Body.String(), "namespace Golem uses for phase labels") {
					t.Error("rejection gives the operator no visible explanation")
				}
				return
			}
			if len(repos) != 1 {
				t.Fatalf("expected exactly one repo row, got %d", len(repos))
			}
			if repos[0].Label != tc.wantStored {
				t.Errorf("Label = %q, want %q", repos[0].Label, tc.wantStored)
			}
		})
	}
}
