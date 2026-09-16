package server

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"html/template"
	"io/fs"
	"net/http"
	"path"
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

// openScriptTagRE matches every <script ...> opening tag on its own,
// independent of how (or whether) inlineScriptRE finds a matching close.
// HTML terminates a script element at a case-insensitive "</script"
// followed by any of "\t\n\f />=" per the HTML5 spec — not only the exact
// literal "</script>" inlineScriptRE requires. A tag closed some other way
// (e.g. "</script >" or "</script/>") would make inlineScriptRE silently
// skip that pair (or worse, swallow through to a later, unrelated
// "</script>"), producing a missing or wrong hash with no error. Comparing
// this count against the number of hashes actually extracted (see
// extractInlineScriptHashes) turns that silent under-production into a
// startup error instead.
var openScriptTagRE = regexp.MustCompile(`(?i)<script([^>]*)>`)

// InlineScriptHashes walks fsys recursively for *.html files (including a
// partials/ subdirectory) and returns a sorted, de-duplicated list of CSP
// script-src source tokens ("'sha256-<base64>'"), one per distinct inline
// <script> body found. Scripts carrying a `src` attribute are skipped — they
// load external code, and hashing their (empty) inline body would be
// meaningless.
//
// fsys must be the filesystem the app actually renders from — in
// production that is ui.TemplateFS(), the //go:embed'd filesystem baked
// into the binary, not a path read from disk. Hashing a disk copy is wrong
// even where the files happen to exist: a stale or locally-modified working
// copy yields a policy that authorizes bytes nobody serves, or blocks bytes
// that are served, and a container image that ships only the compiled
// binary (as this project's does) has no disk copy at all. Taking an fs.FS
// makes the hashes correct by construction, because they come from the same
// bytes the renderer parses, and callers that do want a disk tree (e.g.
// scratch test fixtures) can still supply one via os.DirFS.
//
// The hash is computed over the exact bytes the browser receives, not the
// source bytes between '>' and '</script>': html/template elides JavaScript
// comments while rendering (escape.go's stateJSLineCmt drops a "//..." line
// comment entirely; stateJSBlockCmt collapses a "/* */" block comment to a
// single space or newline), so an inline script containing a comment is
// served with bytes that differ from its source — hashing the source would
// compute a token the browser never matches, silently blocking the script.
// Each body is round-tripped through html/template in its own <script>
// context (see renderScriptBody) before hashing, reproducing the exact
// rendered bytes.
//
// An error is returned — never a nil/empty slice — if fsys cannot be
// walked, if a script body cannot be rendered, if a script body contains a
// template action (see extractInlineScriptHashes), or if the number of
// hashes extracted from a file doesn't match its number of qualifying
// opening tags. Swallowing any of these and returning fewer hashes than
// exist would silently produce a policy that blocks that script; the
// caller (see Server.Routes) is expected to fall back to CSP mode "off"
// rather than ship that.
func InlineScriptHashes(fsys fs.FS) ([]string, error) {
	if fsys == nil {
		return nil, fmt.Errorf("csp: template filesystem is nil")
	}

	seen := make(map[string]struct{})
	walkErr := fs.WalkDir(fsys, ".", func(name string, d fs.DirEntry, err error) error {
		if err != nil {
			return fmt.Errorf("csp: walk %s: %w", name, err)
		}
		if d.IsDir() || !strings.EqualFold(path.Ext(d.Name()), ".html") {
			return nil
		}
		data, err := fs.ReadFile(fsys, name)
		if err != nil {
			return fmt.Errorf("csp: read %s: %w", name, err)
		}
		found, err := extractInlineScriptHashes(data)
		if err != nil {
			return fmt.Errorf("csp: %s: %w", name, err)
		}
		for _, hash := range found {
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
// bytes, hashing the rendered bytes (see renderScriptBody) rather than the
// source bytes.
//
// Two conditions are treated as errors rather than silently producing a
// partial or wrong result:
//
//   - A body OR an opening-tag attribute string containing "{{" is
//     rejected outright: that marks a template action, whose rendered
//     bytes depend on per-request data, so no startup-time hash could ever
//     be correct for it. Attributes are checked for the same reason the
//     body is, and for a second, sharper one: renderScriptBody rebuilds
//     its wrapper open tag from the SOURCE attrs but slices the RENDERED
//     output using that source tag's length. A template action in attrs
//     would change the rendered tag's length without changing the slice
//     offset, silently producing a wrong hash with no error — precisely
//     the failure class fix round 2 exists to close. Rejecting "{{" in
//     attrs here means renderScriptBody is never called with attrs that
//     could trigger it.
//   - The number of hashes produced must equal the number of <script>
//     opening tags without a src attribute (openScriptTagRE) found in the
//     same bytes. inlineScriptRE requires a literal "</script>" to close a
//     block, but HTML itself terminates a script element at "</script"
//     followed by any of "\t\n\f />=" — a tag closed one of those other
//     ways would otherwise be silently missed (or worse, folded into a
//     neighboring script's body) with no error at all.
func extractInlineScriptHashes(data []byte) ([]string, error) {
	var hashes []string
	for _, m := range inlineScriptRE.FindAllSubmatch(data, -1) {
		attrs, body := m[1], m[2]
		if srcAttrRE.Match(attrs) {
			continue
		}
		if bytes.Contains(attrs, []byte("{{")) || bytes.Contains(body, []byte("{{")) {
			return nil, fmt.Errorf(`inline <script> (or its opening tag's attributes) contains "{{": ` +
				`its rendered bytes would depend on per-request data, so no startup-time hash can be correct`)
		}
		rendered, err := renderScriptBody(attrs, body)
		if err != nil {
			return nil, err
		}
		sum := sha256.Sum256(rendered)
		hashes = append(hashes, "'sha256-"+base64.StdEncoding.EncodeToString(sum[:])+"'")
	}

	openWithoutSrc := 0
	for _, m := range openScriptTagRE.FindAllSubmatch(data, -1) {
		if !srcAttrRE.Match(m[1]) {
			openWithoutSrc++
		}
	}
	if len(hashes) != openWithoutSrc {
		return nil, fmt.Errorf("found %d <script> opening tag(s) without a src attribute "+
			"but extracted %d inline script body/bodies — a non-standard closing tag "+
			`(e.g. "</script >" or "</script/>") may have been missed`, openWithoutSrc, len(hashes))
	}

	return hashes, nil
}

// renderScriptBody returns the bytes html/template actually emits for an
// inline <script>'s body, by parsing and executing that element in
// isolation as its own tiny template (with attrs preserved, so e.g. a
// type="application/json" script is classified the same way it would be in
// the real page). The element opens a fresh JS parsing context regardless
// of what surrounds it, so the isolated result is byte-identical to the
// same script rendered as part of the real page — verified against live
// full-page renders for every inline script this project ships as of fix
// round 2 (see task-19-report.md).
//
// The caller has already rejected any body OR attrs containing "{{" (see
// extractInlineScriptHashes), so this executes with nil data and no custom
// FuncMap: there are no actions left to evaluate anywhere in the
// reconstructed tag, only literal text for the escaper to normalize. That
// guarantee matters here specifically: openTag below is built from the
// SOURCE attrs, and the rendered body is recovered by slicing the
// executed output at len(openTag) from the start — if attrs rendered to a
// different length than its source (which a template action would cause),
// that offset would be wrong and this would silently return the wrong
// bytes rather than error.
func renderScriptBody(attrs, body []byte) ([]byte, error) {
	const closeTag = "</script>"
	openTag := "<script" + string(attrs) + ">"
	tmpl, err := template.New("csp-inline-script").Parse(openTag + string(body) + closeTag)
	if err != nil {
		return nil, fmt.Errorf("parse inline script for rendering: %w", err)
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, nil); err != nil {
		return nil, fmt.Errorf("render inline script: %w", err)
	}
	out := buf.Bytes()
	if len(out) < len(openTag)+len(closeTag) {
		return nil, fmt.Errorf("rendered inline script is shorter than its own wrapper tags")
	}
	return out[len(openTag) : len(out)-len(closeTag)], nil
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
//
// img-src allows any HTTPS host ("https:"), not a targeted allowlist, for
// GitHub-hosted issue images. Fix round 2 tried the targeted approach —
// user-images.githubusercontent.com plus a path-scoped
// github.com/user-attachments/ — but the format GitHub writes into issue
// bodies today, github.com/user-attachments/assets/<uuid>, 302-redirects
// to a github-production-user-asset-*.s3.amazonaws.com host. Per CSP3,
// every hop of a redirect is matched again, and past the first hop only
// scheme and host are checked (the path is skipped) — so that S3 host,
// which matches nothing in a targeted list, gets the image blocked at the
// redirect. The targeted approach produced exactly the broken images it
// was meant to prevent. Hardcoding the S3 bucket host was rejected: it's a
// GitHub implementation detail that can change without notice, silently
// re-breaking images again.
//
// The cost is real and is accepted deliberately: an <img> in rendered
// issue content fires on view, so a maliciously authored issue body could
// use it as a tracking pixel — leaking the viewing operator's IP, user
// agent, and the timing of internal review to whoever authored the issue.
// This is judged acceptable because script-src already blocks execution
// and DOMPurify sanitizes the markup before it's ever rendered, so the
// residual risk is a tracking pixel, not data exfiltration: an injected
// image's URL is fixed at authoring time and cannot carry any information
// the attacker didn't already have. Broken images are, empirically, the
// failure most likely to get the whole policy switched off — which would
// forfeit the script-execution protection that actually matters — so a
// slightly permissive img-src is the better trade.
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
		"img-src 'self' data: https:",
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
