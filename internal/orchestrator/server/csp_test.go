package server_test

import (
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"testing/fstest"

	"golang.org/x/crypto/bcrypt"

	"github.com/leonp92/golem/internal/orchestrator/auth"
	"github.com/leonp92/golem/internal/orchestrator/db"
	"github.com/leonp92/golem/internal/orchestrator/server"
	"github.com/leonp92/golem/internal/orchestrator/sse"
	"github.com/leonp92/golem/internal/orchestrator/ui"
	ws "github.com/leonp92/golem/internal/orchestrator/ws"
)

// TestTemplateScanFindsExactlySixInlineScripts is a narrow sanity/regression
// pin: Amendment 4 enumerates exactly six inline <script> blocks (two in
// layout.html, one each in dashboard.html, github_settings.html,
// ticket_detail.html, ticket_new.html), scanned from ui.TemplateFS() — the
// //go:embed'd filesystem the renderer itself uses, not a disk copy that
// could differ from it.
//
// This does NOT prove the resulting hashes authorize what a browser
// actually receives — see the doc comment below on why an earlier version
// of this test claimed that and was wrong, and TestCSPAuthorizesEveryRenderedInlineScript,
// which is the test that actually proves it.
func TestTemplateScanFindsExactlySixInlineScripts(t *testing.T) {
	hashes, err := server.InlineScriptHashes(ui.TemplateFS())
	if err != nil {
		t.Fatalf("InlineScriptHashes: %v", err)
	}
	if len(hashes) != 6 {
		t.Errorf("found %d distinct inline scripts, want 6 (see Amendment 4's enumeration): %v", len(hashes), hashes)
	}
}

// TestCSPAuthorizesEveryRenderedInlineScript is the load-bearing test.
//
// A prior version of this test (then named TestPolicyCoversEveryInlineScript)
// computed hashes with InlineScriptHashes, built a policy from those same
// hashes with BuildPolicy, and asserted the policy contained them —
// tautological: BuildPolicy has no way to NOT include a hash it's handed.
// It could not have caught fix round 2's Critical (html/template silently
// eliding JS comments while rendering, so 4 of 6 inline scripts were hashed
// from the wrong bytes and blocked in "enforce" on every real page).
//
// This test instead proves the actual acceptance criterion: it stands up
// server.Routes() over a real in-memory DB, fetches real pages through the
// real HTTP stack, independently extracts whatever inline <script> bytes
// the response body ACTUALLY contains (using regexes defined here, not
// borrowed from csp.go — the scanner under test must not also be the
// scanner doing the checking), hashes those bytes directly with no further
// processing (they are already final, rendered bytes), and asserts each one
// is authorized by the Content-Security-Policy header on that SAME
// response. If the policy were ever built from source bytes instead of
// rendered bytes again, this test fails.
func TestCSPAuthorizesEveryRenderedInlineScript(t *testing.T) {
	gdb, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}

	passwordHash, err := bcrypt.GenerateFromPassword([]byte("pw"), bcrypt.DefaultCost)
	if err != nil {
		t.Fatalf("bcrypt: %v", err)
	}
	user := db.User{Username: "admin", PasswordHash: string(passwordHash)}
	if err := gdb.Create(&user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	// A shem registered with a repo makes /tickets/new's AvailableRepos
	// non-empty, which is what puts that page's inline <script> (inside
	// {{if .AvailableRepos}}) on the page at all.
	shem := db.Shem{Name: "shem1", APIKeyHash: "x", Repos: `["https://github.com/example/repo.git"]`}
	if err := gdb.Create(&shem).Error; err != nil {
		t.Fatalf("create shem: %v", err)
	}
	ticket := db.Ticket{
		RepoRemote:  "https://github.com/example/repo.git",
		BaseBranch:  "main",
		Title:       "test ticket",
		Branch:      "golem/test-ticket",
		Description: "desc",
		Phase:       "unassigned",
	}
	if err := gdb.Create(&ticket).Error; err != nil {
		t.Fatalf("create ticket: %v", err)
	}

	srv := server.New(gdb, ws.NewHub(), sse.NewBroker(), false, "")
	srv.CSPMode = "enforce"
	handler := srv.Routes()

	sessionRec := httptest.NewRecorder()
	if err := auth.CreateSession(gdb, sessionRec, user.ID, false); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	cookies := sessionRec.Result().Cookies()
	if len(cookies) == 0 {
		t.Fatal("CreateSession set no session cookie")
	}
	sessionCookie := cookies[0]

	// Deliberately re-declared here rather than exported from csp.go: this
	// test's job is to check csp.go's output against an independently
	// derived view of what's actually on the page.
	inlineScriptRE := regexp.MustCompile(`(?is)<script([^>]*)>(.*?)</script>`)
	srcAttrRE := regexp.MustCompile(`(?i)\bsrc\s*=`)

	pages := []string{
		"/login",
		"/dashboard",
		"/shems",
		"/tickets/new",
		"/settings/github",
		"/tickets/" + ticket.ID,
	}

	for _, path := range pages {
		t.Run(path, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, path, nil)
			req.AddCookie(sessionCookie)
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, req)

			if w.Code != http.StatusOK {
				t.Fatalf("GET %s: status = %d, want 200 (body: %s)", path, w.Code, w.Body.String())
			}
			policy := w.Header().Get("Content-Security-Policy")
			if policy == "" {
				t.Fatalf("GET %s: no Content-Security-Policy header on the response", path)
			}

			body := w.Body.Bytes()
			matches := inlineScriptRE.FindAllSubmatch(body, -1)
			checked := 0
			for _, m := range matches {
				attrs, scriptBody := m[1], m[2]
				if srcAttrRE.Match(attrs) {
					continue // external <script src=...>, not an inline body to authorize
				}
				checked++
				sum := sha256.Sum256(scriptBody)
				token := "'sha256-" + base64.StdEncoding.EncodeToString(sum[:]) + "'"
				if !strings.Contains(policy, token) {
					t.Errorf("GET %s: rendered inline <script> (%d bytes) hashes to %s, "+
						"which is NOT in the response's own Content-Security-Policy header "+
						"— this script is silently blocked in enforce mode\npolicy: %s",
						path, len(scriptBody), token, policy)
				}
			}
			if checked == 0 {
				t.Errorf("GET %s: found no inline <script> blocks in the response body — "+
					"the page didn't render the way this test expected, so it isn't "+
					"actually checking anything", path)
			}
		})
	}
}

// TestNoNativeInlineEventHandlers guards the prerequisite this whole header
// depends on: a single native inline handler (onchange=, onclick=, ...)
// would force script-src 'unsafe-inline', re-permitting exactly the
// injected <img onerror=...> vector this header exists to block.
// hx-on::after-request is htmx's own attribute, not a native DOM handler,
// and is deliberately not matched here — it stays, covered by
// 'unsafe-eval'. This scans ui.TemplateFS() — what's actually embedded and
// served — not a disk copy that could differ from it.
func TestNoNativeInlineEventHandlers(t *testing.T) {
	handlerRE := regexp.MustCompile(`\son(change|click|load|error|submit|input|focus|blur)=`)
	fsys := ui.TemplateFS()
	err := fs.WalkDir(fsys, ".", func(name string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(name, ".html") {
			return nil
		}
		data, err := fs.ReadFile(fsys, name)
		if err != nil {
			return err
		}
		if m := handlerRE.FindString(string(data)); m != "" {
			t.Errorf("%s: found native inline event handler %q", name, m)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk template FS: %v", err)
	}
}

func TestInlineScriptHashes_NilFSReturnsError(t *testing.T) {
	hashes, err := server.InlineScriptHashes(nil)
	if err == nil {
		t.Fatal("want error for a nil fs.FS, got nil")
	}
	if len(hashes) != 0 {
		t.Errorf("want no hashes alongside an error, got %v", hashes)
	}
}

// brokenFS is an fs.FS whose every Open fails, simulating an unreadable
// filesystem (e.g. a corrupt embed, or a mount that went away).
type brokenFS struct{ err error }

func (b brokenFS) Open(string) (fs.File, error) { return nil, b.err }

func TestInlineScriptHashes_WalkErrorReturnsError(t *testing.T) {
	hashes, err := server.InlineScriptHashes(brokenFS{err: errors.New("boom")})
	if err == nil {
		t.Fatal("want error when the filesystem can't be walked, got nil")
	}
	if len(hashes) != 0 {
		t.Errorf("want no hashes alongside an error, got %v", hashes)
	}
}

// TestInlineScriptHashes_ExactBytesAndSrcSkip covers two things at once:
// only a <script> tag WITHOUT a src attribute is hashed, and — for a body
// with no JS comment in it, so rendering is a byte-identical no-op — the
// hash is computed over the exact bytes between '>' and '</script>' with no
// trimming or whitespace normalization of its own. (Bodies that DO contain
// a comment are covered separately by
// TestInlineScriptHashes_HashesRenderedBytesNotSourceBytes, below, which is
// the fix round 2 regression test.) fstest.MapFS stands in for
// ui.TemplateFS() so this doesn't depend on the real template tree.
func TestInlineScriptHashes_ExactBytesAndSrcSkip(t *testing.T) {
	const inlineBody = "\n  console.log('hi');\n  var x = 1;\n"
	page := "<html><head>" +
		`<script src="https://cdn.example.com/lib.js"></script>` +
		"<script>" + inlineBody + "</script>" +
		"</head></html>"
	fsys := fstest.MapFS{
		"templates/page.html": &fstest.MapFile{Data: []byte(page)},
	}

	hashes, err := server.InlineScriptHashes(fsys)
	if err != nil {
		t.Fatalf("InlineScriptHashes: %v", err)
	}
	if len(hashes) != 1 {
		t.Fatalf("want exactly 1 hash (the external <script src> must be skipped), got %d: %v", len(hashes), hashes)
	}

	sum := sha256.Sum256([]byte(inlineBody))
	want := "'sha256-" + base64.StdEncoding.EncodeToString(sum[:]) + "'"
	if hashes[0] != want {
		t.Errorf("hash = %s, want %s (exact unmodified bytes)", hashes[0], want)
	}
}

// TestInlineScriptHashes_HashesRenderedBytesNotSourceBytes is the fix
// round 2 regression test. html/template elides JavaScript comments while
// rendering (a line comment is dropped entirely; a block comment collapses
// to a single space, or a single newline if the comment itself spanned a
// line break), so the source bytes and the served bytes differ for any
// inline script containing a comment. Hashing the source, as an earlier
// version of InlineScriptHashes did, silently blocks that script in
// "enforce" mode. Each expected "rendered" value below was independently
// confirmed against html/template's actual output before being hardcoded
// here (see task-19-report.md fix round 2 for the verification transcript).
func TestInlineScriptHashes_HashesRenderedBytesNotSourceBytes(t *testing.T) {
	tests := []struct {
		name     string
		src      string
		rendered string
	}{
		{
			name:     "line comment is dropped, trailing newline kept",
			src:      "var x = 1; // line comment\nvar y = 2;",
			rendered: "var x = 1; \nvar y = 2;",
		},
		{
			name:     "block comment spanning a line break collapses to one newline",
			src:      "var x = 1;\n/* block\ncomment */\nvar y = 2;",
			rendered: "var x = 1;\n\n\nvar y = 2;",
		},
		{
			name:     "single-line block comment collapses to one space",
			src:      "var x = 1; /* single line block */ var y = 2;",
			rendered: "var x = 1;   var y = 2;",
		},
		{
			name:     "// inside a string literal is not a comment and is preserved",
			src:      "var a = 'not // a comment';",
			rendered: "var a = 'not // a comment';",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fsys := fstest.MapFS{
				"templates/page.html": &fstest.MapFile{Data: []byte("<html><script>" + tt.src + "</script></html>")},
			}
			hashes, err := server.InlineScriptHashes(fsys)
			if err != nil {
				t.Fatalf("InlineScriptHashes: %v", err)
			}
			if len(hashes) != 1 {
				t.Fatalf("want 1 hash, got %d: %v", len(hashes), hashes)
			}

			wantSum := sha256.Sum256([]byte(tt.rendered))
			want := "'sha256-" + base64.StdEncoding.EncodeToString(wantSum[:]) + "'"
			if hashes[0] != want {
				t.Errorf("hash = %s, want %s (sha256 of the RENDERED bytes %q)", hashes[0], want, tt.rendered)
			}

			if tt.src != tt.rendered {
				srcSum := sha256.Sum256([]byte(tt.src))
				srcHash := "'sha256-" + base64.StdEncoding.EncodeToString(srcSum[:]) + "'"
				if hashes[0] == srcHash {
					t.Errorf("hash matches the SOURCE bytes %q instead of the rendered bytes — this is exactly the fix round 2 regression", tt.src)
				}
			}
		})
	}
}

// TestInlineScriptHashes_RejectsScriptWithTemplateAction covers IMPORTANT 2
// from the fix round 2 review: nothing previously stopped someone writing
// e.g. {{.CSRFToken}} inside an inline <script>. Its rendered bytes would
// then be request-dependent, so no startup-time hash could ever be correct,
// and it would be silently blocked on every request with an otherwise-green
// test suite. This must fail loudly instead.
func TestInlineScriptHashes_RejectsScriptWithTemplateAction(t *testing.T) {
	fsys := fstest.MapFS{
		"templates/page.html": &fstest.MapFile{Data: []byte("<html><script>var csrf = '{{.CSRFToken}}';</script></html>")},
	}
	hashes, err := server.InlineScriptHashes(fsys)
	if err == nil {
		t.Fatal("want error for an inline <script> containing a template action, got nil")
	}
	if len(hashes) != 0 {
		t.Errorf("want no hashes alongside an error, got %v", hashes)
	}
	if !strings.Contains(err.Error(), "{{") {
		t.Errorf("error should name the offending \"{{\", got: %v", err)
	}
}

// TestInlineScriptHashes_NonStandardClosingTagErrors covers IMPORTANT 3:
// HTML terminates a <script> element at a case-insensitive "</script"
// followed by any of "\t\n\f />=", not only the literal "</script>"
// inlineScriptRE requires. "</script >" (a space before '>') is one such
// terminator a browser honors that the regex does not, so this file's only
// <script> pair is invisible to inlineScriptRE even though its opening tag
// exists — exactly the silent under-production this check exists to catch.
func TestInlineScriptHashes_NonStandardClosingTagErrors(t *testing.T) {
	fsys := fstest.MapFS{
		"templates/page.html": &fstest.MapFile{Data: []byte("<html><script>doThing();</script ></html>")},
	}
	hashes, err := server.InlineScriptHashes(fsys)
	if err == nil {
		t.Fatal("want error when a <script> is closed with a non-literal-'</script>' terminator, got nil")
	}
	if len(hashes) != 0 {
		t.Errorf("want no hashes alongside an error, got %v", hashes)
	}
}

func TestInlineScriptHashes_WalksPartialsSubdirectory(t *testing.T) {
	partial := "<div><script>doPartialThing();</script></div>"
	fsys := fstest.MapFS{
		"templates/partials/widget.html": &fstest.MapFile{Data: []byte(partial)},
	}

	hashes, err := server.InlineScriptHashes(fsys)
	if err != nil {
		t.Fatalf("InlineScriptHashes: %v", err)
	}
	if len(hashes) != 1 {
		t.Fatalf("want 1 hash from templates/partials, got %d: %v", len(hashes), hashes)
	}
}

func TestInlineScriptHashes_IgnoresNonHTMLFiles(t *testing.T) {
	fsys := fstest.MapFS{
		"templates/notes.txt": &fstest.MapFile{Data: []byte("<script>should not be scanned</script>")},
		"templates/page.html": &fstest.MapFile{Data: []byte("<html><script>real();</script></html>")},
	}

	hashes, err := server.InlineScriptHashes(fsys)
	if err != nil {
		t.Fatalf("InlineScriptHashes: %v", err)
	}
	if len(hashes) != 1 {
		t.Fatalf("want 1 hash (only the .html file scanned), got %d: %v", len(hashes), hashes)
	}
}

func TestInlineScriptHashes_Deterministic(t *testing.T) {
	fsys := ui.TemplateFS()
	first, err := server.InlineScriptHashes(fsys)
	if err != nil {
		t.Fatalf("InlineScriptHashes (first): %v", err)
	}
	second, err := server.InlineScriptHashes(fsys)
	if err != nil {
		t.Fatalf("InlineScriptHashes (second): %v", err)
	}
	if strings.Join(first, ",") != strings.Join(second, ",") {
		t.Errorf("InlineScriptHashes is not deterministic:\n  first:  %v\n  second: %v", first, second)
	}
	for i := 1; i < len(first); i++ {
		if first[i-1] > first[i] {
			t.Errorf("hashes not sorted: %v", first)
			break
		}
	}
}

func TestBuildPolicy(t *testing.T) {
	tests := []struct {
		name    string
		hashes  []string
		wantAll []string // substrings that must appear somewhere in the policy
	}{
		{
			name:   "no inline scripts",
			hashes: nil,
			wantAll: []string{
				"default-src 'self'",
				"script-src 'self' 'unsafe-eval'",
				"style-src 'self' 'unsafe-inline' https://cdn.jsdelivr.net",
				"img-src 'self' data: https://user-images.githubusercontent.com https://github.com/user-attachments/",
				"font-src 'self' data:",
				"connect-src 'self' https://api.iconify.design https://api.simplesvg.com https://api.unisvg.com",
				"base-uri 'self'",
				"form-action 'self'",
				"frame-ancestors 'none'",
				"object-src 'none'",
				"https://cdn.tailwindcss.com",
				"https://unpkg.com",
				"https://cdn.jsdelivr.net",
				"https://code.iconify.design",
			},
		},
		{
			name:   "inline script hashes are threaded into script-src",
			hashes: []string{"'sha256-AAAA'", "'sha256-BBBB'"},
			wantAll: []string{
				"'sha256-AAAA'",
				"'sha256-BBBB'",
				"script-src 'self' 'unsafe-eval' 'sha256-AAAA' 'sha256-BBBB'",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			policy := server.BuildPolicy(tt.hashes)
			for _, want := range tt.wantAll {
				if !strings.Contains(policy, want) {
					t.Errorf("policy missing %q\nfull policy: %s", want, policy)
				}
			}
			scriptSrc := policy[strings.Index(policy, "script-src"):]
			if end := strings.Index(scriptSrc, ";"); end != -1 {
				scriptSrc = scriptSrc[:end]
			}
			if strings.Contains(scriptSrc, "'unsafe-inline'") {
				t.Error("script-src must never contain 'unsafe-inline'")
			}
		})
	}
}

// TestCSPMiddleware covers the three documented modes: enforce sets
// Content-Security-Policy; report-only sets Content-Security-Policy-Report-Only
// and not the enforcing header name; off sets neither.
func TestCSPMiddleware(t *testing.T) {
	const policy = "default-src 'self'"

	tests := []struct {
		name           string
		mode           string
		wantEnforce    string
		wantReportOnly string
	}{
		{name: "enforce", mode: "enforce", wantEnforce: policy},
		{name: "report-only", mode: "report-only", wantReportOnly: policy},
		{name: "off", mode: "off"},
		{name: "empty mode defaults to enforce", mode: "", wantEnforce: policy},
		{name: "unrecognized mode defaults to enforce", mode: "bogus", wantEnforce: policy},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
			})
			handler := server.CSPMiddleware(policy, tt.mode)(next)

			req := httptest.NewRequest(http.MethodGet, "/", nil)
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			if got := rec.Header().Get("Content-Security-Policy"); got != tt.wantEnforce {
				t.Errorf("Content-Security-Policy = %q, want %q", got, tt.wantEnforce)
			}
			if got := rec.Header().Get("Content-Security-Policy-Report-Only"); got != tt.wantReportOnly {
				t.Errorf("Content-Security-Policy-Report-Only = %q, want %q", got, tt.wantReportOnly)
			}
			if tt.mode == "off" {
				if rec.Header().Get("Content-Security-Policy") != "" || rec.Header().Get("Content-Security-Policy-Report-Only") != "" {
					t.Error("mode=off must set neither CSP header")
				}
			}
			if rec.Code != http.StatusOK {
				t.Errorf("next handler was not called: got status %d", rec.Code)
			}
		})
	}
}
