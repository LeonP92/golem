package ui_test

import (
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/leonp92/golem/internal/orchestrator/auth"
	"github.com/leonp92/golem/internal/orchestrator/db"
	"github.com/leonp92/golem/internal/orchestrator/rbac"
	"github.com/leonp92/golem/internal/orchestrator/ui"
)

// The dashboard's per-row "Open ticket" button opens in a new tab.
//
// The dashboard is a worklist an operator reads down, and it refreshes
// itself on a timer; following a ticket in the same tab loses that place and
// the scroll position, and coming back re-renders from the top. The button
// already used the arrow-out-of-a-box icon, which promises a new tab, so the
// markup was contradicting its own affordance.
//
// rel="noopener" travels with it. These are same-origin links so there is no
// window.opener risk today, but the pairing is the convention everywhere
// else in these templates (the repository, issue and pull request links in
// ticket_detail.html), and a target="_blank" without it is the shape that
// becomes a problem the first time one points somewhere else.
func TestDashboardOpenTicketButtonOpensInANewTab(t *testing.T) {
	gdb, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	user := db.User{Username: "leon", PasswordHash: "x", Role: string(rbac.RoleAdmin)}
	if err := gdb.Create(&user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	ticket := db.Ticket{
		ID: "6f1f0f0e-1111-4222-8333-444455556666", RepoRemote: "https://github.com/acme/widgets",
		Branch: "ticket/x-6f1f0f0e", BaseBranch: "main", Description: "d",
		Title: "Add rate limiting", Phase: "implement",
	}
	if err := gdb.Create(&ticket).Error; err != nil {
		t.Fatalf("create ticket: %v", err)
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

	req := httptest.NewRequest(http.MethodGet, "/dashboard", nil)
	req.AddCookie(rec.Result().Cookies()[0])
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("GET /dashboard = %d: %s", w.Code, w.Body.String())
	}

	// A row has two links to the same ticket: the title, which is the
	// primary in-place click target, and this icon button. Pick the button
	// by its title attribute — matching on the href alone finds the title
	// link first and would assert against the wrong element.
	var attrs string
	for _, m := range regexp.MustCompile(`(?s)<a href="/tickets/`+ticket.ID+`"(.*?)>`).
		FindAllStringSubmatch(w.Body.String(), -1) {
		if strings.Contains(m[1], `title="Open ticket`) {
			attrs = m[1]
		}
	}
	if attrs == "" {
		t.Fatal(`no "Open ticket" button on the dashboard row`)
	}
	if !strings.Contains(attrs, `target="_blank"`) {
		t.Error(`the "Open ticket" button has no target="_blank"; it replaces the ` +
			`worklist the operator is reading instead of opening beside it`)
	}
	if !strings.Contains(attrs, `rel="noopener"`) {
		t.Error(`the "Open ticket" button has target="_blank" without rel="noopener"`)
	}
}
