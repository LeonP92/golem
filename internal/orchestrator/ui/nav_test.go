package ui_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/leonp92/golem/internal/orchestrator/auth"
	"github.com/leonp92/golem/internal/orchestrator/db"
	"github.com/leonp92/golem/internal/orchestrator/rbac"
	"github.com/leonp92/golem/internal/orchestrator/ui"
)

// TestNavIsCompleteOnEveryNavPage catches a whole class of bug rather than one
// instance of it.
//
// The nav gates each control on data the page supplies: Users on
// CanManageUsers, New Ticket on CanCreateTicket, the account link on
// CurrentUser. h.base() provides all three, so a page that builds its render
// map from scratch silently loses nav items — not with an error, just with
// controls quietly missing. /settings/github did exactly that: it predated
// base() and was written with a literal four-key map, so an admin looking at
// the GitHub page could not see the Users link, the New Ticket button, or
// their own account menu.
//
// Asserting on the rendered page, for every nav-bearing route, is what makes
// this catch the next page somebody adds the same way.
func TestNavIsCompleteOnEveryNavPage(t *testing.T) {
	gdb, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	user := db.User{Username: "root", PasswordHash: "x", Role: string(rbac.RoleAdmin)}
	if err := gdb.Create(&user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
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

	// Every page that renders the nav. The ticket detail page is deliberately
	// nav-less (it renders with an empty Nav), so it is not listed.
	pages := []string{"/dashboard", "/shems", "/settings/github", "/tickets/new", "/users", "/settings/security"}

	// One marker per permission-gated nav control, with the render-map key
	// that supplies it, so a failure says which key the page forgot.
	controls := []struct{ marker, key string }{
		{`href="/users"`, "CanManageUsers"},
		{`href="/tickets/new"`, "CanCreateTicket"},
		{`href="/settings"`, "CurrentUser"},
	}

	for _, page := range pages {
		t.Run(page, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, page, nil)
			req.AddCookie(cookie)
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, req)
			if w.Code != http.StatusOK {
				t.Fatalf("GET %s = %d: %s", page, w.Code, w.Body.String())
			}
			body := w.Body.String()
			if !strings.Contains(body, `aria-label="Main navigation"`) {
				t.Fatalf("GET %s rendered no nav at all; add it to the nav-less list or supply Nav", page)
			}
			for _, c := range controls {
				if !strings.Contains(body, c.marker) {
					t.Errorf("nav is missing %s — this page's render map does not supply %s "+
						"(start it from h.base(r, nav) rather than a literal map)", c.marker, c.key)
				}
			}
		})
	}
}
