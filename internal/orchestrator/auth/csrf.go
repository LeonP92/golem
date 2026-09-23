package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"net/http"
	"strings"
	"time"
)

// CSRFHeader is the request header a browser-issued, state-changing request
// carries its CSRF token in. htmx requests get it from the htmx:configRequest
// listener in layout.html; hand-written fetch() calls set it explicitly.
const CSRFHeader = "X-CSRF-Token"

// CSRFFormField is the form field name a plain (non-htmx) HTML form carries
// its CSRF token in, for forms submitted by the browser's own navigation
// rather than by JavaScript.
const CSRFFormField = "csrf_token"

// csrfDerivationLabel domain-separates the CSRF token from the session
// token hash stored in db.Session. Both are SHA-256 of the same secret
// cookie value, so without a distinct label they would be the same string,
// and a CSRF token rendered into a page would be a usable session-lookup
// key. With it, neither value can be derived from the other.
const csrfDerivationLabel = "golem-csrf-v1|"

const csrfTokenKey contextKey = "csrf_token"

// LoginCSRFCookie holds the pre-session nonce that the login form's token is
// derived from. Login is the one state-changing POST that cannot use
// CSRFTokenForSession, because the whole point of it is that no session
// exists yet.
const LoginCSRFCookie = "golem_login_csrf"

// loginCSRFDerivationLabel domain-separates the login token from the session
// token exactly as csrfDerivationLabel does, so a value minted before sign-in
// can never be mistaken for one minted after it.
const loginCSRFDerivationLabel = "golem-login-csrf-v1|"

// loginCSRFTTL bounds how long a rendered login form stays submittable. Long
// enough that a page left open over a coffee break still works, short enough
// that a nonce captured from a shared machine is not useful tomorrow.
const loginCSRFTTL = 2 * time.Hour

// maxCSRFFormBytes caps how much of a multipart body ParseMultipartForm may
// buffer in memory while the middleware looks for the token field. Only
// reached for multipart submissions, which the dashboard does not currently
// make; urlencoded bodies go through ParseForm's own 10MB limit.
const maxCSRFFormBytes = 1 << 20

// CSRFTokenForSession derives the CSRF token for a session from that
// session's raw cookie value.
//
// The token is derived rather than stored, deliberately: it needs no new
// column, no migration, and no second store to keep in sync with session
// expiry, and it is automatically invalidated the moment the session is —
// there is no window in which a stale token still verifies. The session
// cookie is HttpOnly, so page content (including anything that survives
// markdown sanitization) cannot read the input to this derivation; an
// attacker who can only make the operator's browser issue same-origin
// requests cannot compute the token, which is precisely the capability
// finding S1's injected-form attack has.
//
// An empty sessionToken yields an empty token, which RequireCSRF treats as
// "no token" and rejects — never as "matches the empty presented token".
func CSRFTokenForSession(sessionToken string) string {
	if sessionToken == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(csrfDerivationLabel + sessionToken))
	return hex.EncodeToString(sum[:])
}

// CSRFToken returns the CSRF token for the session that authenticated r, as
// stored in the request context by RequireSession. Returns "" when r did not
// come through RequireSession — templates render that as an empty token, and
// RequireCSRF rejects it.
func CSRFToken(r *http.Request) string {
	t, _ := r.Context().Value(csrfTokenKey).(string)
	return t
}

// RequireCSRF middleware: rejects a state-changing request whose CSRF token
// is missing or does not match the one derived from its session cookie.
// Must be installed INSIDE RequireSession, which is what puts the expected
// token in the request context.
//
// Safe methods (GET, HEAD, OPTIONS, TRACE) pass through untouched — they are
// not state-changing, and requiring a token on them would break ordinary
// navigation.
//
// SameSite=Lax on the session cookie does not substitute for this. The
// attack this closes (finding S1) injects a <form action="/api/..."> into
// the dashboard's own markdown sink: the form is served by, and posts to,
// the same origin, so every SameSite rule is satisfied and the browser
// attaches the session cookie as usual. Only a secret the injected markup
// cannot read distinguishes the operator's click on a real control from
// their click on an attacker's invisible overlay.
func RequireCSRF(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions, http.MethodTrace:
			next.ServeHTTP(w, r)
			return
		}

		expected := CSRFToken(r)
		if expected == "" {
			// No session token to derive from: fail closed rather than
			// comparing "" against a presented "" and passing.
			http.Error(w, "forbidden: missing CSRF token", http.StatusForbidden)
			return
		}

		presented := r.Header.Get(CSRFHeader)
		if presented == "" {
			var err error
			presented, err = csrfTokenFromBody(r)
			if err != nil {
				http.Error(w, "bad request: "+err.Error(), http.StatusBadRequest)
				return
			}
		}

		if subtle.ConstantTimeCompare([]byte(presented), []byte(expected)) != 1 {
			http.Error(w, "forbidden: invalid CSRF token", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// csrfTokenFromBody reads the CSRF token out of a form-encoded request body.
// It reads r.PostForm, never r.Form, so a token supplied in the URL query
// string is not accepted: an injected <form action="...?csrf_token=..."> must
// not be able to satisfy the check with a value the attacker chose, and the
// same query-vs-body distinction is what finding S1's second half is about.
//
// Bodies that are not form-encoded (JSON, in particular) must use the header;
// decoding them here would consume the body the real handler needs.
func csrfTokenFromBody(r *http.Request) (string, error) {
	ct := r.Header.Get("Content-Type")
	switch {
	case strings.HasPrefix(ct, "application/x-www-form-urlencoded"):
		if err := r.ParseForm(); err != nil {
			return "", err
		}
	case strings.HasPrefix(ct, "multipart/form-data"):
		if err := r.ParseMultipartForm(maxCSRFFormBytes); err != nil {
			return "", err
		}
	default:
		return "", nil
	}
	return r.PostForm.Get(CSRFFormField), nil
}

// CSRFTokenForLogin derives the login form's token from the pre-session nonce
// in LoginCSRFCookie, mirroring CSRFTokenForSession: same construction, same
// fail-closed treatment of an empty input, different domain-separation label.
func CSRFTokenForLogin(nonce string) string {
	if nonce == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(loginCSRFDerivationLabel + nonce))
	return hex.EncodeToString(sum[:])
}

// EnsureLoginCSRF returns the token to render into the login form, minting a
// nonce and setting its cookie when the request does not already carry one.
// Re-rendering the login page with a nonce already in hand keeps the existing
// one, so two tabs open on /login do not invalidate each other.
//
// The cookie is HttpOnly: the form field is rendered server-side, so nothing
// needs to read the nonce from JavaScript, and an attacker who can inject
// markup into a page therefore cannot read it either. That is the whole
// mechanism — they can make the browser SEND the cookie, but not learn what
// value to put in the field.
func EnsureLoginCSRF(w http.ResponseWriter, r *http.Request, secure bool) (string, error) {
	if c, err := r.Cookie(LoginCSRFCookie); err == nil && c.Value != "" {
		return CSRFTokenForLogin(c.Value), nil
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	nonce := hex.EncodeToString(raw)
	http.SetCookie(w, &http.Cookie{
		Name:     LoginCSRFCookie,
		Value:    nonce,
		Path:     "/",
		Expires:  time.Now().Add(loginCSRFTTL),
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
	})
	return CSRFTokenForLogin(nonce), nil
}

// VerifyLoginCSRF reports whether r carries a login token matching its own
// nonce cookie. Like the session path it reads PostForm, never Form, so a
// token supplied in the query string cannot satisfy it.
//
// A missing cookie is a failure, not a pass: that is the exact shape of the
// cross-site POST this exists to refuse, since a browser following an
// attacker's form submission sends whatever cookies it holds but the attacker
// never caused a login page to be rendered.
func VerifyLoginCSRF(r *http.Request) bool {
	c, err := r.Cookie(LoginCSRFCookie)
	if err != nil || c.Value == "" {
		return false
	}
	expected := CSRFTokenForLogin(c.Value)
	if expected == "" {
		return false
	}
	presented := r.PostForm.Get(CSRFFormField)
	if presented == "" {
		presented = r.Header.Get(CSRFHeader)
	}
	return subtle.ConstantTimeCompare([]byte(presented), []byte(expected)) == 1
}

// ClearLoginCSRF expires the pre-session nonce. Called once a session exists,
// after which the session-derived token governs every state-changing request
// and leaving the login nonce in place would serve no purpose.
func ClearLoginCSRF(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:    LoginCSRFCookie,
		Value:   "",
		Path:    "/",
		MaxAge:  -1,
		Expires: time.Unix(0, 0),
	})
}
