package ui_test

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"gorm.io/gorm"

	"github.com/leonp92/golem/internal/orchestrator/auth"
	"github.com/leonp92/golem/internal/orchestrator/db"
	"github.com/leonp92/golem/internal/orchestrator/rbac"
	"github.com/leonp92/golem/internal/orchestrator/ui"
)

// setupEpicUITest wires the full template set the way production does —
// these tests render real pages, because a handler that crashes template
// execution only fails the user, not the build.
func setupEpicUITest(t *testing.T) (*gorm.DB, *http.ServeMux, *http.Cookie) {
	t.Helper()
	gdb, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	user := db.User{Username: "leon", PasswordHash: "x", Role: string(rbac.RoleAdmin)}
	if err := gdb.Create(&user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	rec := httptest.NewRecorder()
	if err := auth.CreateSession(gdb, rec, user.ID, false); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	tmpls, err := ui.LoadTemplates()
	if err != nil {
		t.Fatalf("LoadTemplates: %v", err)
	}
	mux := http.NewServeMux()
	ui.NewHandlersWithMap(gdb, tmpls, false).RegisterRoutes(mux)
	return gdb, mux, rec.Result().Cookies()[0]
}

func epicFormPost(t *testing.T, mux *http.ServeMux, cookie *http.Cookie, path string, form url.Values) *httptest.ResponseRecorder {
	t.Helper()
	form.Set(auth.CSRFFormField, auth.CSRFTokenForSession(cookie.Value))
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(cookie)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	return w
}

func epicGet(t *testing.T, mux *http.ServeMux, cookie *http.Cookie, path string) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, withSession(httptest.NewRequest(http.MethodGet, path, nil), cookie))
	if w.Code != http.StatusOK {
		t.Fatalf("GET %s = %d", path, w.Code)
	}
	return w
}

const epicUITestPlan = `{"requirements":[{"id":"R1","text":"model"}],
"nodes":[{"key":"T1","title":"schema","acceptance_commands":["go test ./..."],
"requirement_ids":["R1"]}],"edges":[]}`

// The full human loop the design asks for: create the epic, save a plan
// revision, approve it — with the approve form carrying the exact hash the
// page rendered, so approval can never drift onto content nobody saw.
func TestEpicPages_CreateReviseApprove(t *testing.T) {
	gdb, mux, cookie := setupEpicUITest(t)

	w := epicFormPost(t, mux, cookie, "/epics/new", url.Values{
		"repo_remote":     {"https://github.com/org/repo"},
		"base_branch":     {"main"},
		"title":           {"Billing rebuild"},
		"spec_markdown":   {"# Spec\n\n## Requirements\nR1 ..."},
		"branch_strategy": {"epic-branch"},
		"delivery_target": {"feature-branch"},
	})
	if w.Code != http.StatusFound || !strings.HasPrefix(w.Header().Get("Location"), "/epics/") {
		t.Fatalf("create = %d %q", w.Code, w.Header().Get("Location"))
	}
	detail := w.Header().Get("Location")

	if body := epicGet(t, mux, cookie, "/epics").Body.String(); !strings.Contains(body, "Billing rebuild") {
		t.Fatalf("list page missing the new epic")
	}

	w = epicFormPost(t, mux, cookie, detail+"/revisions", url.Values{"plan_json": {epicUITestPlan}})
	if w.Code != http.StatusFound || strings.Contains(w.Header().Get("Location"), "error=") {
		t.Fatalf("revision = %d %q", w.Code, w.Header().Get("Location"))
	}
	if body := epicGet(t, mux, cookie, detail).Body.String(); !strings.Contains(body, "awaiting approval") {
		t.Fatalf("detail after revision missing awaiting-approval badge")
	}

	var rev db.EpicPlanRevision
	if err := gdb.Where("revision = 1").First(&rev).Error; err != nil {
		t.Fatalf("stored revision: %v", err)
	}
	w = epicFormPost(t, mux, cookie, detail+"/approve", url.Values{
		"revision":  {"1"},
		"plan_hash": {rev.PlanHash},
	})
	if w.Code != http.StatusFound || strings.Contains(w.Header().Get("Location"), "error=") {
		t.Fatalf("approve = %d %q", w.Code, w.Header().Get("Location"))
	}
	body := epicGet(t, mux, cookie, detail).Body.String()
	if !strings.Contains(body, "approved by leon") {
		t.Fatalf("detail after approve missing approval badge")
	}

	// A new revision clears the approval — the page must stop showing it.
	w = epicFormPost(t, mux, cookie, detail+"/revisions", url.Values{"plan_json": {epicUITestPlan}})
	if strings.Contains(w.Header().Get("Location"), "error=") {
		t.Fatalf("second revision rejected: %q", w.Header().Get("Location"))
	}
	if body := epicGet(t, mux, cookie, detail).Body.String(); !strings.Contains(body, "awaiting approval") {
		t.Fatalf("detail after second revision should be awaiting approval again")
	}
}

func TestEpicRevisionRejectsInvalidPlan(t *testing.T) {
	_, mux, cookie := setupEpicUITest(t)
	w := epicFormPost(t, mux, cookie, "/epics/new", url.Values{
		"repo_remote":   {"https://github.com/org/repo"},
		"base_branch":   {"main"},
		"title":         {"x"},
		"spec_markdown": {"spec"},
	})
	detail := w.Header().Get("Location")

	w = epicFormPost(t, mux, cookie, detail+"/revisions", url.Values{"plan_json": {"not json"}})
	if loc := w.Header().Get("Location"); !strings.Contains(loc, "error=") {
		t.Fatalf("unparseable plan did not surface an error: %q", loc)
	}
	// A structurally invalid plan fails closed too, with the validator's
	// reasons on the page.
	w = epicFormPost(t, mux, cookie, detail+"/revisions", url.Values{"plan_json": {`{"requirements":[],"nodes":[],"edges":[]}`}})
	if loc := w.Header().Get("Location"); !strings.Contains(loc, "error=") {
		t.Fatalf("invalid plan did not surface an error: %q", loc)
	}
}

func TestEpicApproveRejectsWrongHash(t *testing.T) {
	_, mux, cookie := setupEpicUITest(t)
	w := epicFormPost(t, mux, cookie, "/epics/new", url.Values{
		"repo_remote":   {"https://github.com/org/repo"},
		"base_branch":   {"main"},
		"title":         {"x"},
		"spec_markdown": {"spec"},
	})
	detail := w.Header().Get("Location")
	epicFormPost(t, mux, cookie, detail+"/revisions", url.Values{"plan_json": {epicUITestPlan}})

	w = epicFormPost(t, mux, cookie, detail+"/approve", url.Values{
		"revision":  {"1"},
		"plan_hash": {"deadbeef"},
	})
	if loc := w.Header().Get("Location"); !strings.Contains(loc, "error=") {
		t.Fatalf("approve with wrong hash did not fail closed: %q", loc)
	}
	if body := epicGet(t, mux, cookie, detail).Body.String(); !strings.Contains(body, "awaiting approval") {
		t.Fatalf("wrong-hash approval should leave the plan awaiting approval")
	}
}

// The run half of the detail page: approve, start, watch the children
// appear as ordinary linked tickets with readiness, and receipts move the
// integrated count.
func TestEpicDetail_StartAndProgress(t *testing.T) {
	gdb, mux, cookie := setupEpicUITest(t)

	w := epicFormPost(t, mux, cookie, "/epics/new", url.Values{
		"repo_remote":   {"https://github.com/org/repo"},
		"base_branch":   {"main"},
		"title":         {"Run test"},
		"spec_markdown": {"spec"},
	})
	detail := w.Header().Get("Location")
	epicFormPost(t, mux, cookie, detail+"/revisions", url.Values{"plan_json": {epicUITestPlan}})

	var rev db.EpicPlanRevision
	if err := gdb.Where("revision = 1").First(&rev).Error; err != nil {
		t.Fatalf("revision: %v", err)
	}
	w = epicFormPost(t, mux, cookie, detail+"/approve", url.Values{
		"revision": {"1"}, "plan_hash": {rev.PlanHash},
	})
	if strings.Contains(w.Header().Get("Location"), "error=") {
		t.Fatalf("approve failed: %q", w.Header().Get("Location"))
	}
	// Approved and still awaiting start: the page offers Start.
	if body := epicGet(t, mux, cookie, detail).Body.String(); !strings.Contains(body, "Start run") {
		t.Fatal("no Start button after approval")
	}

	w = epicFormPost(t, mux, cookie, detail+"/start", url.Values{})
	if loc := w.Header().Get("Location"); strings.Contains(loc, "error=") {
		t.Fatalf("start failed: %q", loc)
	}
	body := epicGet(t, mux, cookie, detail).Body.String()
	if !strings.Contains(body, "0 / 1</strong> integrated") {
		t.Fatal("no progress line after start")
	}
	// The child is a real ticket, linked from its row.
	var child db.Ticket
	if err := gdb.First(&child, "epic_id = ?", rev.EpicID).Error; err != nil {
		t.Fatalf("child: %v", err)
	}
	if !strings.Contains(body, "/tickets/"+child.ID) {
		t.Fatal("child ticket not linked")
	}
	// Starting again is a no-op (the button stays gone anyway).
	w = epicFormPost(t, mux, cookie, detail+"/start", url.Values{})
	if loc := w.Header().Get("Location"); strings.Contains(loc, "error=") {
		t.Fatalf("repeat start failed: %q", loc)
	}
	var n int64
	gdb.Model(&db.Ticket{}).Where("epic_id = ?", rev.EpicID).Count(&n)
	if n != 1 {
		t.Fatalf("repeat start produced %d children", n)
	}
}
