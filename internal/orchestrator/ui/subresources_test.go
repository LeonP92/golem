package ui_test

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/leonp92/golem/internal/orchestrator/auth"
	"github.com/leonp92/golem/internal/orchestrator/db"
	"github.com/leonp92/golem/internal/orchestrator/ui"
)

// Every byte of executable code this dashboard runs, beyond what the binary
// itself serves, arrives from a public CDN. Round 3 pinned DOMPurify because
// it is the control on the markdown sink. That reasoning does not stop at the
// control: `marked` sits directly upstream of it and produces the HTML
// DOMPurify is handed, so a marked release — or a compromise of the registry
// or CDN serving it — moves what the sanitizer has to defend against. htmx
// runs with 'unsafe-eval' granted and drives every state-changing request on
// the page. Pinning the sanitizer while its input floats leaves the weaker
// half of the pair exposed.
//
// So this file's rule is the page's rule, not one dependency's: every external
// subresource is pinned to an exact version and integrity-checked, and any
// exception is named here with the reason it cannot be.

// subresourceTagRE matches every <script> or <link> tag whose src/href is an
// absolute https URL. Discovering the tags rather than listing them is the
// point: a new CDN script added without a pin and a hash fails this test on
// the day it is added, which a hand-maintained list would not do.
var subresourceTagRE = regexp.MustCompile(`(?is)<(script|link)\b[^>]*\b(?:src|href)="(https://[^"]+)"[^>]*>`)

// exactVersionRE matches a semver-shaped segment anywhere in the URL. It
// covers every CDN spelling in use here: jsdelivr and unpkg's "@1.2.3",
// Iconify's "/3/3.1.0/", and Tailwind's "/3.4.17".
var exactVersionRE = regexp.MustCompile(`[@/]\d+\.\d+\.\d+([/?]|$)`)

// noSRIHosts are hosts that cannot be integrity-checked, with the reason.
// Adding to this map is a deliberate act; the test below asserts that such a
// tag is still version-pinned and that it does NOT carry the attributes,
// because half-applied SRI is worse than none — see the Tailwind entry.
var noSRIHosts = map[string]string{
	"cdn.tailwindcss.com": "the Play CDN sends no access-control-allow-origin header, so " +
		"crossorigin=\"anonymous\" would make the browser block the script outright, and " +
		"an integrity attribute without it is not honoured on a cross-origin script. " +
		"Measured: curl -I https://cdn.tailwindcss.com/3.4.17 returns no CORS header. " +
		"The version is pinned, which at least removes the floating redirect.",
}

type subresource struct {
	tag  string
	kind string
	url  string
	host string
}

// renderedPage returns the bytes a browser actually receives for /dashboard,
// not the template source. This follows server/csp.go's lesson: html/template
// is between the file and the wire, so a check over the source can assert
// something the browser never sees. Here the tags happen to be static text and
// come through unchanged — but "happen to" is the part worth proving rather
// than assuming.
func renderedPage(t *testing.T) string {
	t.Helper()
	gdb, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	user := db.User{Username: "leon", PasswordHash: "x"}
	gdb.Create(&user)
	rec := httptest.NewRecorder()
	if err := auth.CreateSession(gdb, rec, user.ID, false); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	cookie := rec.Result().Cookies()[0]

	tmpls, err := ui.LoadTemplates()
	if err != nil {
		t.Fatalf("LoadTemplates: %v", err)
	}
	mux := http.NewServeMux()
	ui.NewHandlersWithMap(gdb, tmpls, false).RegisterRoutes(mux)

	req := httptest.NewRequest(http.MethodGet, "/dashboard", nil)
	req.AddCookie(cookie)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("GET /dashboard: %d", w.Code)
	}
	return w.Body.String()
}

func layoutSubresources(t *testing.T) []subresource {
	t.Helper()
	matches := subresourceTagRE.FindAllStringSubmatch(renderedPage(t), -1)
	if len(matches) == 0 {
		t.Fatal("found no external <script>/<link> tags in the rendered page; the scan is broken")
	}
	out := make([]subresource, 0, len(matches))
	for _, m := range matches {
		url := m[2]
		host := strings.SplitN(strings.TrimPrefix(url, "https://"), "/", 2)[0]
		out = append(out, subresource{tag: m[0], kind: m[1], url: url, host: host})
	}
	return out
}

// TestExternalSubresourcesArePinnedAndIntegrityChecked is the page-wide rule.
func TestExternalSubresourcesArePinnedAndIntegrityChecked(t *testing.T) {
	integrityRE := regexp.MustCompile(`integrity="sha(256|384|512)-[A-Za-z0-9+/]+={0,2}"`)
	crossoriginRE := regexp.MustCompile(`crossorigin="anonymous"`)

	for _, sub := range layoutSubresources(t) {
		t.Run(sub.url, func(t *testing.T) {
			if !exactVersionRE.MatchString(sub.url) {
				t.Errorf("%s is not pinned to an exact version. A floating specifier lets a "+
					"future release change what this page runs, with nothing in CI able to "+
					"notice.\ntag: %s", sub.url, sub.tag)
			}

			if why, exempt := noSRIHosts[sub.host]; exempt {
				if integrityRE.MatchString(sub.tag) || crossoriginRE.MatchString(sub.tag) {
					t.Errorf("%s carries integrity/crossorigin, but %s is listed as unable to "+
						"support them: %s\nIf that has changed, remove the entry from "+
						"noSRIHosts rather than leaving both in place.", sub.url, sub.host, why)
				}
				return
			}

			if !integrityRE.MatchString(sub.tag) {
				t.Errorf("%s has no integrity hash, so a registry or CDN compromise silently "+
					"replaces code this page executes.\ntag: %s", sub.url, sub.tag)
			}
			if !crossoriginRE.MatchString(sub.tag) {
				t.Errorf("%s has no crossorigin=\"anonymous\"; a cross-origin subresource "+
					"without it is not integrity-checked at all.\ntag: %s", sub.url, sub.tag)
			}
		})
	}
}

// TestMarkdownPipelineFailsClosedWhenASubresourceIsRefused is the other half of
// pinning: an integrity mismatch must degrade, not expose.
//
// If DOMPurify is refused, the guard sees no callable sanitize and takes the
// textContent path. If marked is refused, `marked.parse` throws a
// ReferenceError from inside the try, and the catch takes the same path. Both
// land on plain text; neither lands on unsanitized HTML. That is what makes a
// hash mismatch an availability problem rather than a security one, and it is
// why pinning is safe to do here at all.
func TestMarkdownPipelineFailsClosedWhenASubresourceIsRefused(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("templates", "layout.html"))
	if err != nil {
		t.Fatalf("read layout.html: %v", err)
	}
	body := string(src)
	start := strings.Index(body, "function renderMarkdown(root)")
	if start == -1 {
		t.Fatal("renderMarkdown is gone from layout.html")
	}
	fn := body[start:]
	if end := strings.Index(fn, "\n    }"); end != -1 {
		fn = fn[:end]
	}

	parse := strings.Index(fn, "marked.parse(")
	if parse == -1 {
		t.Fatal("renderMarkdown no longer calls marked.parse")
	}
	try := strings.LastIndex(fn[:parse], "try {")
	if try == -1 {
		t.Error("marked.parse is not inside a try block, so a refused marked.min.js throws " +
			"out of renderMarkdown instead of degrading to plain text")
	}
	catch := strings.Index(fn[parse:], "catch")
	if catch == -1 {
		t.Fatal("no catch after marked.parse")
	}
	if !strings.Contains(fn[parse+catch:], "textContent = source") {
		t.Error("the catch around marked.parse does not fall back to plain text")
	}
	if !strings.Contains(fn, "typeof DOMPurify === 'undefined' ? null : DOMPurify") {
		t.Error("renderMarkdown no longer guards against DOMPurify being absent, which is what " +
			"a refused purify.min.js looks like")
	}
}
