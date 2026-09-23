package auth_test

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/leonp92/golem/internal/orchestrator/auth"
	"github.com/leonp92/golem/internal/orchestrator/db"
)

// TestCSRFTokenForSession covers the properties the derivation has to hold,
// not its implementation: it must be stable for one session, different for
// different sessions, must never equal the session token itself, must never
// equal the token hash stored in db.Session (which is also SHA-256 of the
// same cookie value — without domain separation the CSRF token rendered into
// every page would be a usable session-lookup key), and must be empty for an
// empty input so RequireCSRF can reject it rather than match it.
func TestCSRFTokenForSession(t *testing.T) {
	tests := []struct {
		name         string
		sessionToken string
		wantEmpty    bool
	}{
		{name: "empty session token yields empty csrf token", sessionToken: "", wantEmpty: true},
		{name: "typical 32-byte hex session token", sessionToken: strings.Repeat("ab", 32)},
		{name: "adjacent session token", sessionToken: strings.Repeat("ab", 31) + "ac"},
		{name: "short token", sessionToken: "x"},
	}

	seen := make(map[string]string)
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := auth.CSRFTokenForSession(tc.sessionToken)
			if tc.wantEmpty {
				if got != "" {
					t.Fatalf("token = %q, want empty", got)
				}
				return
			}
			if got == "" {
				t.Fatal("token is empty for a non-empty session token")
			}
			if got == auth.CSRFTokenForSession(tc.sessionToken+"!") {
				t.Error("token does not change with the session token")
			}
			if again := auth.CSRFTokenForSession(tc.sessionToken); again != got {
				t.Errorf("token is not stable: %q then %q", got, again)
			}
			if got == tc.sessionToken {
				t.Error("CSRF token equals the session token — it is rendered into pages, " +
					"so it must not be the session secret")
			}
			sum := sha256.Sum256([]byte(tc.sessionToken))
			if got == hex.EncodeToString(sum[:]) {
				t.Error("CSRF token equals db.Session.TokenHash — no domain separation")
			}
			if prev, ok := seen[got]; ok {
				t.Errorf("collision: %q and %q derive the same token", prev, tc.sessionToken)
			}
			seen[got] = tc.sessionToken
		})
	}
}

// TestRequireCSRF covers the middleware's contract directly, including the
// two cases that fail open if written carelessly: no session in the context
// at all (the expected token is "" and must not match a presented "") and a
// token supplied in the URL query string, which an injected same-origin form
// controls entirely.
func TestRequireCSRF(t *testing.T) {
	gdb, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	user := db.User{Username: "alice", PasswordHash: "x"}
	gdb.Create(&user)
	rec := httptest.NewRecorder()
	if err := auth.CreateSession(gdb, rec, user.ID, false); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	cookie := rec.Result().Cookies()[0]
	token := auth.CSRFTokenForSession(cookie.Value)

	protected := auth.RequireSession(gdb)(auth.RequireCSRF(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusNoContent)
		})))
	// Deliberately NOT wrapped in RequireSession: no expected token exists.
	unsessioned := auth.RequireCSRF(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	tests := []struct {
		name     string
		handler  http.Handler
		method   string
		target   string
		ct       string
		body     string
		header   string
		wantCode int
	}{
		{
			name: "GET passes without a token", handler: protected,
			method: http.MethodGet, target: "/", wantCode: http.StatusNoContent,
		},
		{
			name: "POST with the correct header token", handler: protected,
			method: http.MethodPost, target: "/", header: token, wantCode: http.StatusNoContent,
		},
		{
			name: "POST with the correct form-field token", handler: protected,
			method: http.MethodPost, target: "/",
			ct: "application/x-www-form-urlencoded", body: auth.CSRFFormField + "=" + token,
			wantCode: http.StatusNoContent,
		},
		{
			name: "POST with no token", handler: protected,
			method: http.MethodPost, target: "/", wantCode: http.StatusForbidden,
		},
		{
			name: "POST with a wrong token", handler: protected,
			method: http.MethodPost, target: "/", header: "wrong", wantCode: http.StatusForbidden,
		},
		{
			name: "POST with the token only in the query string", handler: protected,
			method: http.MethodPost, target: "/?" + auth.CSRFFormField + "=" + token,
			ct: "application/x-www-form-urlencoded", body: "", wantCode: http.StatusForbidden,
		},
		{
			name: "POST outside any session fails closed", handler: unsessioned,
			method: http.MethodPost, target: "/", wantCode: http.StatusForbidden,
		},
		{
			name:    "POST outside any session with an empty presented token fails closed",
			handler: unsessioned, method: http.MethodPost, target: "/",
			ct: "application/x-www-form-urlencoded", body: auth.CSRFFormField + "=",
			wantCode: http.StatusForbidden,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, tc.target, strings.NewReader(tc.body))
			if tc.ct != "" {
				req.Header.Set("Content-Type", tc.ct)
			}
			if tc.header != "" {
				req.Header.Set(auth.CSRFHeader, tc.header)
			}
			req.AddCookie(cookie)
			w := httptest.NewRecorder()
			tc.handler.ServeHTTP(w, req)
			if w.Code != tc.wantCode {
				t.Errorf("status = %d, want %d: %s", w.Code, tc.wantCode, w.Body.String())
			}
		})
	}
}

// TestCSRFToken_AbsentWithoutSession pins that a request that never went
// through RequireSession reports no token at all, rather than a zero value
// that could accidentally verify.
func TestCSRFToken_AbsentWithoutSession(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	if got := auth.CSRFToken(req); got != "" {
		t.Errorf("CSRFToken = %q, want empty for a request with no session", got)
	}
}
