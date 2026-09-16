package api_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/leonp92/golem/internal/orchestrator/auth"
	"github.com/leonp92/golem/internal/orchestrator/db"
)

// withSession attaches a session cookie AND the CSRF token derived from it,
// which is what a real browser sends: the token is rendered into the page by
// ui.Handlers.render and put on the wire by layout.html's htmx:configRequest
// listener. Tests that want to exercise the *absence* of a token add the
// cookie directly instead.
func withSession(req *http.Request, cookie *http.Cookie) *http.Request {
	req.AddCookie(cookie)
	req.Header.Set(auth.CSRFHeader, auth.CSRFTokenForSession(cookie.Value))
	return req
}

// TestTicketAction_IgnoresQueryStringAction covers half of finding S1: the
// dispatcher used r.FormValue, and ParseForm merges the URL query into r.Form
// for a POST, so an action supplied entirely in the query string of an
// otherwise EMPTY body was honoured. That is what makes an injected
// <form action="/api/tickets/<id>/actions?action=start"> work at all —
// DOMPurify strips name= from <input>, so the attacker cannot put fields in
// the body, only in the form's own URL.
//
// Verified against HEAD 51d9526 before the fix: status 204, phase
// "needs-attention".
func TestTicketAction_IgnoresQueryStringAction(t *testing.T) {
	tests := []struct {
		name  string
		query string
		body  string
	}{
		{name: "empty body, action in query", query: "?action=needs-attention", body: ""},
		{name: "empty body, start in query", query: "?action=start", body: ""},
		{
			name:  "body action wins over a conflicting query action",
			query: "?action=close",
			body:  "action=needs-attention",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h, mux, cookie := setupActionTest(t)
			ticket := db.Ticket{ID: "qs-1", RepoRemote: "https://github.com/org/repo",
				BaseBranch: "main", Branch: "b", Title: "t", Description: "d", Phase: "implement"}
			if err := h.DB.Create(&ticket).Error; err != nil {
				t.Fatalf("seed ticket: %v", err)
			}

			req := httptest.NewRequest(http.MethodPost,
				"/api/tickets/qs-1/actions"+tc.query, strings.NewReader(tc.body))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, withSession(req, cookie))

			var after db.Ticket
			if err := h.DB.First(&after, "id = ?", "qs-1").Error; err != nil {
				t.Fatalf("reload ticket: %v", err)
			}

			if tc.body == "" {
				if w.Code != http.StatusBadRequest {
					t.Errorf("status = %d, want 400 (query-string action must not be read)", w.Code)
				}
				if after.Phase != "implement" {
					t.Errorf("phase = %q, want unchanged %q", after.Phase, "implement")
				}
				return
			}
			// A body action must still work, and must be the one that applies.
			if w.Code != http.StatusNoContent {
				t.Errorf("status = %d, want 204: %s", w.Code, w.Body.String())
			}
			if after.Phase != "needs-attention" {
				t.Errorf("phase = %q, want %q (the BODY action, not the query one)",
					after.Phase, "needs-attention")
			}
		})
	}
}

// TestTicketAction_RequiresCSRFToken covers the other half of finding S1.
// The attack is same-origin — the injected form is served by, and posts to,
// the dashboard's own origin — so SameSite=Lax on the session cookie does
// nothing. Only a secret the injected markup cannot read separates a real
// click on a real control from a click on an attacker's invisible overlay.
//
// Verified against HEAD 51d9526 before the fix: status 204 with no token at
// all, and the phase changed.
func TestTicketAction_RequiresCSRFToken(t *testing.T) {
	tests := []struct {
		name        string
		contentType string
		body        string
		// decorate applies the attacker's / browser's token, if any.
		decorate  func(req *http.Request, cookie *http.Cookie)
		wantCode  int
		wantPhase string
	}{
		{
			name:        "form post with no token at all",
			contentType: "application/x-www-form-urlencoded",
			body:        "action=needs-attention",
			decorate:    func(req *http.Request, c *http.Cookie) {},
			wantCode:    http.StatusForbidden,
			wantPhase:   "implement",
		},
		{
			name:        "json post with no token at all",
			contentType: "application/json",
			body:        `{"action":"needs-attention"}`,
			decorate:    func(req *http.Request, c *http.Cookie) {},
			wantCode:    http.StatusForbidden,
			wantPhase:   "implement",
		},
		{
			name:        "wrong token in the header",
			contentType: "application/x-www-form-urlencoded",
			body:        "action=needs-attention",
			decorate: func(req *http.Request, c *http.Cookie) {
				req.Header.Set(auth.CSRFHeader, "not-the-token")
			},
			wantCode:  http.StatusForbidden,
			wantPhase: "implement",
		},
		{
			name:        "token guessed from the session cookie value verbatim",
			contentType: "application/x-www-form-urlencoded",
			body:        "action=needs-attention",
			decorate: func(req *http.Request, c *http.Cookie) {
				// The CSRF token must not simply BE the session token.
				req.Header.Set(auth.CSRFHeader, c.Value)
			},
			wantCode:  http.StatusForbidden,
			wantPhase: "implement",
		},
		{
			name:        "token in the query string is not accepted",
			contentType: "application/x-www-form-urlencoded",
			body:        "action=needs-attention",
			decorate: func(req *http.Request, c *http.Cookie) {
				req.URL.RawQuery = auth.CSRFFormField + "=" + auth.CSRFTokenForSession(c.Value)
			},
			wantCode:  http.StatusForbidden,
			wantPhase: "implement",
		},
		{
			name:        "correct token in the header",
			contentType: "application/x-www-form-urlencoded",
			body:        "action=needs-attention",
			decorate: func(req *http.Request, c *http.Cookie) {
				req.Header.Set(auth.CSRFHeader, auth.CSRFTokenForSession(c.Value))
			},
			wantCode:  http.StatusNoContent,
			wantPhase: "needs-attention",
		},
		{
			name:        "correct token in the form body",
			contentType: "application/x-www-form-urlencoded",
			body:        "action=needs-attention&" + auth.CSRFFormField + "=PLACEHOLDER",
			decorate:    func(req *http.Request, c *http.Cookie) {},
			wantCode:    http.StatusNoContent,
			wantPhase:   "needs-attention",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h, mux, cookie := setupActionTest(t)
			ticket := db.Ticket{ID: "csrf-1", RepoRemote: "https://github.com/org/repo",
				BaseBranch: "main", Branch: "b", Title: "t", Description: "d", Phase: "implement"}
			if err := h.DB.Create(&ticket).Error; err != nil {
				t.Fatalf("seed ticket: %v", err)
			}

			body := strings.ReplaceAll(tc.body, "PLACEHOLDER", auth.CSRFTokenForSession(cookie.Value))
			req := httptest.NewRequest(http.MethodPost, "/api/tickets/csrf-1/actions",
				strings.NewReader(body))
			req.Header.Set("Content-Type", tc.contentType)
			req.AddCookie(cookie)
			tc.decorate(req, cookie)

			w := httptest.NewRecorder()
			mux.ServeHTTP(w, req)

			if w.Code != tc.wantCode {
				t.Errorf("status = %d, want %d: %s", w.Code, tc.wantCode, w.Body.String())
			}
			var after db.Ticket
			if err := h.DB.First(&after, "id = ?", "csrf-1").Error; err != nil {
				t.Fatalf("reload ticket: %v", err)
			}
			if after.Phase != tc.wantPhase {
				t.Errorf("phase = %q, want %q", after.Phase, tc.wantPhase)
			}
		})
	}
}
