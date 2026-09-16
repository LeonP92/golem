package server

import (
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// CSP modes accepted by CSPMiddleware. config.Load validates and normalizes
// a configured csp.mode to one of these before it ever reaches here; the
// switch in CSPMiddleware treats anything else defensively as "enforce" for
// callers (e.g. tests) that build a Server without going through config.Load.
const (
	cspModeReportOnly = "report-only"
	cspModeOff        = "off"
)

// inlineScriptRE matches every <script ...>...</script> block in an HTML
// document, capturing the opening tag's attribute string and the raw body
// between '>' and '</script>'. The (?s) flag makes '.' match newlines so a
// multi-line script body is captured whole; matching is non-greedy so two
// script tags on the same page are captured as separate blocks rather than
// one span running from the first '<script' to the last '</script>'.
var inlineScriptRE = regexp.MustCompile(`(?is)<script([^>]*)>(.*?)</script>`)

// srcAttrRE detects a `src` attribute on a <script> opening tag. A script
// with a src attribute loads external code; hashing its (empty) inline body
// would be meaningless, so such tags are skipped entirely.
var srcAttrRE = regexp.MustCompile(`(?i)\bsrc\s*=`)

// InlineScriptHashes walks dir recursively for *.html files (including a
// partials/ subdirectory) and returns a sorted, de-duplicated list of CSP
// script-src source tokens ("'sha256-<base64>'"), one per distinct inline
// <script> body found. Scripts carrying a `src` attribute are skipped — they
// load external code, and hashing their (empty) inline body would be
// meaningless.
//
// The hash is computed over the exact bytes between '>' and '</script>',
// unmodified: trimming or normalizing whitespace would compute a hash that
// does not match what the browser actually parses, silently blocking that
// script under the resulting policy.
//
// An error is returned — never a nil/empty slice — if dir cannot be read.
// Swallowing that error and returning no hashes would silently produce a
// policy that blocks every inline script on every page; the caller (see
// Server.Routes) is expected to fall back to CSP mode "off" rather than
// ship that.
func InlineScriptHashes(dir string) ([]string, error) {
	info, err := os.Stat(dir)
	if err != nil {
		return nil, fmt.Errorf("csp: stat template dir %s: %w", dir, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("csp: template dir %s is not a directory", dir)
	}

	seen := make(map[string]struct{})
	walkErr := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return fmt.Errorf("csp: walk %s: %w", path, err)
		}
		if d.IsDir() || !strings.EqualFold(filepath.Ext(d.Name()), ".html") {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("csp: read %s: %w", path, err)
		}
		for _, hash := range extractInlineScriptHashes(data) {
			seen[hash] = struct{}{}
		}
		return nil
	})
	if walkErr != nil {
		return nil, walkErr
	}

	hashes := make([]string, 0, len(seen))
	for h := range seen {
		hashes = append(hashes, h)
	}
	sort.Strings(hashes)
	return hashes, nil
}

// extractInlineScriptHashes returns one CSP hash token per inline <script>
// block (i.e. one with no src attribute) found in an HTML document's raw
// bytes.
func extractInlineScriptHashes(data []byte) []string {
	var hashes []string
	for _, m := range inlineScriptRE.FindAllSubmatch(data, -1) {
		attrs, body := m[1], m[2]
		if srcAttrRE.Match(attrs) {
			continue
		}
		sum := sha256.Sum256(body)
		hashes = append(hashes, "'sha256-"+base64.StdEncoding.EncodeToString(sum[:])+"'")
	}
	return hashes
}

// BuildPolicy assembles the orchestrator's Content-Security-Policy directive
// string. scriptHashes are the 'sha256-...' tokens for every inline <script>
// block in the UI templates (see InlineScriptHashes) — each one must be
// present in script-src or the corresponding inline script is blocked by the
// browser.
//
// 'unsafe-eval' is required by Tailwind's Play CDN, which compiles CSS at
// runtime, and by htmx's evaluation of hx-on attribute bodies. It permits
// eval/new Function but does NOT re-permit inline <script> blocks or inline
// event-handler attributes — those still require the hashes above — so the
// protection that matters (blocking an injected <img onerror=...> payload)
// is retained.
//
// style-src keeps 'unsafe-inline' because Tailwind injects <style> elements
// at runtime; style-only injection is a markedly lower-severity class than
// script execution.
//
// connect-src includes Iconify's icon-data API and its two documented
// fallback hosts: the <script> tag is loaded from code.iconify.design, but
// icon SVGs are fetched at runtime from api.iconify.design (falling back to
// api.simplesvg.com / api.unisvg.com) — omitting them renders every icon
// blank.
func BuildPolicy(scriptHashes []string) string {
	scriptSrc := make([]string, 0, 2+len(scriptHashes)+4)
	scriptSrc = append(scriptSrc, "'self'", "'unsafe-eval'")
	scriptSrc = append(scriptSrc, scriptHashes...)
	scriptSrc = append(scriptSrc,
		"https://cdn.tailwindcss.com",
		"https://unpkg.com",
		"https://cdn.jsdelivr.net",
		"https://code.iconify.design",
	)

	directives := []string{
		"default-src 'self'",
		"script-src " + strings.Join(scriptSrc, " "),
		"style-src 'self' 'unsafe-inline' https://cdn.jsdelivr.net",
		"img-src 'self' data:",
		"font-src 'self' data:",
		"connect-src 'self' https://api.iconify.design https://api.simplesvg.com https://api.unisvg.com",
		"base-uri 'self'",
		"form-action 'self'",
		"frame-ancestors 'none'",
		"object-src 'none'",
	}
	return strings.Join(directives, "; ")
}

// CSPMiddleware returns middleware that sets the Content-Security-Policy
// header (or, in report-only mode, Content-Security-Policy-Report-Only)
// before calling next. mode == "off" is a pure pass-through that sets
// neither header — an explicit escape hatch for a deployment where the
// policy breaks something the fix can't reach in time.
func CSPMiddleware(policy, mode string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch mode {
			case cspModeOff:
				// No header set at all.
			case cspModeReportOnly:
				w.Header().Set("Content-Security-Policy-Report-Only", policy)
			default:
				// "enforce" and any unrecognized value. Enforce is the safe
				// default: a policy that protects nothing by default is not
				// protection.
				w.Header().Set("Content-Security-Policy", policy)
			}
			next.ServeHTTP(w, r)
		})
	}
}
