package ui_test

import (
	"net/http"
	"net/http/httptest"
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
