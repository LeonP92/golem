package server_test

import (
	"crypto/sha256"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/leonp92/golem/internal/orchestrator/server"
)

// TestPolicyCoversEveryInlineScript fails if an inline script is added,
// edited, or removed without the policy following. Inline scripts are
// allowed by hash, so a stale policy silently breaks the page.
func TestPolicyCoversEveryInlineScript(t *testing.T) {
	hashes, err := server.InlineScriptHashes(filepath.Join("..", "ui", "templates"))
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
// 'unsafe-eval'.
func TestNoNativeInlineEventHandlers(t *testing.T) {
	root := filepath.Join("..", "ui", "templates")
	handlerRE := regexp.MustCompile(`\son(change|click|load|error|submit|input|focus|blur)=`)
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".html") {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if m := handlerRE.FindString(string(data)); m != "" {
			t.Errorf("%s: found native inline event handler %q", path, m)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
}

func TestInlineScriptHashes_MissingDirReturnsError(t *testing.T) {
	hashes, err := server.InlineScriptHashes(filepath.Join(t.TempDir(), "does-not-exist"))
	if err == nil {
		t.Fatal("want error for a missing directory, got nil")
	}
	if len(hashes) != 0 {
		t.Errorf("want no hashes alongside an error, got %v", hashes)
	}
}

func TestInlineScriptHashes_NotADirectoryReturnsError(t *testing.T) {
	f := filepath.Join(t.TempDir(), "not-a-dir.html")
	if err := os.WriteFile(f, []byte("<html></html>"), 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}
	if _, err := server.InlineScriptHashes(f); err == nil {
		t.Fatal("want error when dir is actually a file, got nil")
	}
}

// TestInlineScriptHashes_ExactBytesAndSrcSkip covers two risk areas at once:
// only a <script> tag WITHOUT a src attribute is hashed, and the hash is
// computed over the exact, unmodified bytes between '>' and '</script>' —
// no trimming or whitespace normalization.
func TestInlineScriptHashes_ExactBytesAndSrcSkip(t *testing.T) {
	dir := t.TempDir()
	const inlineBody = "\n  console.log('hi');\n  var x = 1;\n"
	page := "<html><head>" +
		`<script src="https://cdn.example.com/lib.js"></script>` +
		"<script>" + inlineBody + "</script>" +
		"</head></html>"
	if err := os.WriteFile(filepath.Join(dir, "page.html"), []byte(page), 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}

	hashes, err := server.InlineScriptHashes(dir)
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
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "partials"), 0o755); err != nil {
		t.Fatalf("mkdir partials: %v", err)
	}
	partial := "<div><script>doPartialThing();</script></div>"
	if err := os.WriteFile(filepath.Join(dir, "partials", "widget.html"), []byte(partial), 0o600); err != nil {
		t.Fatalf("write partial: %v", err)
	}

	hashes, err := server.InlineScriptHashes(dir)
	if err != nil {
		t.Fatalf("InlineScriptHashes: %v", err)
	}
	if len(hashes) != 1 {
		t.Fatalf("want 1 hash from templates/partials, got %d: %v", len(hashes), hashes)
	}
}

func TestInlineScriptHashes_Deterministic(t *testing.T) {
	dir := filepath.Join("..", "ui", "templates")
	first, err := server.InlineScriptHashes(dir)
	if err != nil {
		t.Fatalf("InlineScriptHashes (first): %v", err)
	}
	second, err := server.InlineScriptHashes(dir)
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
