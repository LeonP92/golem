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

	"github.com/leonp92/golem/internal/orchestrator/server"
	"github.com/leonp92/golem/internal/orchestrator/ui"
)

// TestPolicyCoversEveryInlineScript fails if an inline script is added,
// edited, or removed without the policy following. Inline scripts are
// allowed by hash, so a stale policy silently breaks the page. Hashing
// ui.TemplateFS() — the //go:embed'd filesystem the renderer itself uses —
// rather than a path on disk means the hashes are correct by construction:
// they come from the exact bytes that get served.
func TestPolicyCoversEveryInlineScript(t *testing.T) {
	hashes, err := server.InlineScriptHashes(ui.TemplateFS())
	if err != nil {
		t.Fatalf("InlineScriptHashes: %v", err)
	}
	if len(hashes) == 0 {
		t.Fatal("found no inline scripts; the scanner is broken, which would " +
			"silently produce a policy that blocks every inline script")
	}
	// Amendment 4 enumerates exactly six inline <script> blocks (two in
	// layout.html, one each in dashboard.html, github_settings.html,
	// ticket_detail.html, ticket_new.html). Pinning the count catches a
	// scanner regression (e.g. matching external <script src=...> tags)
	// that len(hashes) == 0 alone would not.
	if len(hashes) != 6 {
		t.Errorf("found %d distinct inline scripts, want 6 (see Amendment 4's enumeration): %v", len(hashes), hashes)
	}

	policy := server.BuildPolicy(hashes)
	for _, h := range hashes {
		if !strings.Contains(policy, h) {
			t.Errorf("policy omits %s", h)
		}
	}
	if strings.Contains(policy, "'unsafe-inline'") &&
		strings.Contains(policy, "script-src") {
		// style-src may carry unsafe-inline; script-src must not.
		scriptSrc := policy[strings.Index(policy, "script-src"):]
		if end := strings.Index(scriptSrc, ";"); end != -1 {
			scriptSrc = scriptSrc[:end]
		}
		if strings.Contains(scriptSrc, "'unsafe-inline'") {
			t.Error("script-src contains 'unsafe-inline', which re-permits " +
				"injected event handlers and defeats the policy")
		}
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

// TestInlineScriptHashes_ExactBytesAndSrcSkip covers two risk areas at once:
// only a <script> tag WITHOUT a src attribute is hashed, and the hash is
// computed over the exact, unmodified bytes between '>' and '</script>' —
// no trimming or whitespace normalization. fstest.MapFS stands in for
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
				"img-src 'self' data:",
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
