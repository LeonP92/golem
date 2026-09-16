package auth

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"net/http"
	"strings"
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
