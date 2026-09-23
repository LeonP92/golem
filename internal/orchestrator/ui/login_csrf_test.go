package ui_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"golang.org/x/crypto/bcrypt"

	"github.com/leonp92/golem/internal/orchestrator/auth"
	"github.com/leonp92/golem/internal/orchestrator/db"
	"github.com/leonp92/golem/internal/orchestrator/rbac"
	"github.com/leonp92/golem/internal/orchestrator/ui"
)

// Login is the one state-changing POST that cannot use the session-derived
// CSRF token, because there is no session yet. It was therefore left
// unprotected, and the gap is real: an attacker who can make an operator's
// browser POST /login with the ATTACKER's credentials silently signs that
// operator into the attacker's account. Everything the operator then does —
// the tickets they create, the GitHub issues they approve — happens in an
// account the attacker can read at leisure, and nothing on screen says so.
//
// The fix is a double-submit token minted before the session exists: a
// random nonce in an HttpOnly cookie, and the value derived from it rendered
// into the form. An attacker can make the browser send the cookie but cannot
// read it, so they cannot produce the matching field.
func newLoginMux(t *testing.T) (*http.ServeMux, *db.User) {
	t.Helper()
	gdb, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	hash, err := bcrypt.GenerateFromPassword([]byte("correct-horse"), bcrypt.MinCost)
	if err != nil {
		t.Fatalf("bcrypt: %v", err)
	}
	user := db.User{Username: "victim", PasswordHash: string(hash), Role: string(rbac.RoleAdmin)}
	if err := gdb.Create(&user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	tmpls, err := ui.LoadTemplates()
	if err != nil {
		t.Fatalf("LoadTemplates: %v", err)
	}
	mux := http.NewServeMux()
	ui.NewHandlersWithMap(gdb, tmpls, false).RegisterRoutes(mux)
	return mux, &user
}

// loginPage fetches GET /login and returns the nonce cookie it sets plus the
// token rendered into the form — i.e. exactly what a browser would hold.
func loginPage(t *testing.T, mux *http.ServeMux) (*http.Cookie, string) {
	t.Helper()
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/login", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("GET /login = %d, want 200", w.Code)
	}
	var nonce *http.Cookie
	for _, c := range w.Result().Cookies() {
		if c.Name == auth.LoginCSRFCookie {
			nonce = c
		}
	}
	if nonce == nil {
		t.Fatalf("GET /login set no %s cookie", auth.LoginCSRFCookie)
	}
	body := w.Body.String()
	const marker = `name="csrf_token" value="`
	i := strings.Index(body, marker)
	if i == -1 {
		t.Fatal("the login form carries no csrf_token field")
	}
	rest := body[i+len(marker):]
	return nonce, rest[:strings.Index(rest, `"`)]
}

func postLogin(mux *http.ServeMux, form string, cookie *http.Cookie) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(form))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if cookie != nil {
		req.AddCookie(cookie)
	}
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	return w
}

func sessionCookie(w *httptest.ResponseRecorder) *http.Cookie {
	for _, c := range w.Result().Cookies() {
		if c.Name == "golem_session" && c.Value != "" {
			return c
		}
	}
	return nil
}

func TestLoginRequiresItsOwnCSRFToken(t *testing.T) {
	t.Run("a cross-site POST with no token cannot sign the browser in", func(t *testing.T) {
		mux, _ := newLoginMux(t)
		w := postLogin(mux, "username=victim&password=correct-horse", nil)
		if sessionCookie(w) != nil {
			t.Error("a tokenless POST /login established a session: login CSRF is open")
		}
		if w.Code == http.StatusFound && w.Header().Get("Location") == "/dashboard" {
			t.Error("a tokenless POST /login was treated as a successful sign-in")
		}
	})

	t.Run("the browser's own form still signs in", func(t *testing.T) {
		mux, _ := newLoginMux(t)
		nonce, token := loginPage(t, mux)
		w := postLogin(mux, "username=victim&password=correct-horse&csrf_token="+token, nonce)
		if sessionCookie(w) == nil {
			t.Fatalf("the real login form was refused: %d %s", w.Code, w.Body.String())
		}
		if loc := w.Header().Get("Location"); loc != "/dashboard" {
			t.Errorf("Location = %q, want /dashboard", loc)
		}
	})

	t.Run("a token that does not match the cookie is refused", func(t *testing.T) {
		mux, _ := newLoginMux(t)
		nonce, _ := loginPage(t, mux)
		other, _ := loginPage(t, mux) // a token minted for a different nonce
		_ = other
		w := postLogin(mux, "username=victim&password=correct-horse&csrf_token=deadbeef", nonce)
		if sessionCookie(w) != nil {
			t.Error("a forged token established a session")
		}
	})

	t.Run("a token from another browser's page is refused", func(t *testing.T) {
		mux, _ := newLoginMux(t)
		mine, _ := loginPage(t, mux)
		_, theirToken := loginPage(t, mux)
		w := postLogin(mux, "username=victim&password=correct-horse&csrf_token="+theirToken, mine)
		if sessionCookie(w) != nil {
			t.Error("a token minted for a different nonce was accepted")
		}
	})

	t.Run("the token cannot be supplied in the query string", func(t *testing.T) {
		mux, _ := newLoginMux(t)
		nonce, token := loginPage(t, mux)
		req := httptest.NewRequest(http.MethodPost, "/login?csrf_token="+token,
			strings.NewReader("username=victim&password=correct-horse"))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.AddCookie(nonce)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		if sessionCookie(w) != nil {
			t.Error("a token in the query string was accepted; an injected form could supply it")
		}
	})

	t.Run("wrong credentials with a valid token still fail", func(t *testing.T) {
		mux, _ := newLoginMux(t)
		nonce, token := loginPage(t, mux)
		w := postLogin(mux, "username=victim&password=wrong&csrf_token="+token, nonce)
		if sessionCookie(w) != nil {
			t.Error("a valid CSRF token signed in a wrong password")
		}
	})
}
