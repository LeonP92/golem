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

// The dashboard splits tickets into two tables behind a toggle: the default
// view is the active worklist and leaves closed tickets out, and ?view=closed
// shows only the closed ones. The live poll must re-fetch the view it is on,
// or the closed table would flip back to active after ten seconds.
func TestDashboardSeparatesActiveAndClosedTickets(t *testing.T) {
	gdb, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	user := db.User{Username: "leon", PasswordHash: "x", Role: string(rbac.RoleAdmin)}
	if err := gdb.Create(&user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	for _, tk := range []db.Ticket{
		{ID: "aaaaaaaa-1111-4222-8333-444455556666", Title: "Active one", Phase: "implement"},
		{ID: "bbbbbbbb-1111-4222-8333-444455556666", Title: "Closed one", Phase: "closed"},
	} {
		tk.RepoRemote, tk.Branch, tk.BaseBranch, tk.Description = "https://github.com/acme/w", "b-"+tk.ID[:8], "main", "d"
		if err := gdb.Create(&tk).Error; err != nil {
			t.Fatalf("create ticket: %v", err)
		}
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

	get := func(path string) string {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.AddCookie(rec.Result().Cookies()[0])
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("GET %s = %d: %s", path, w.Code, w.Body.String())
		}
		return w.Body.String()
	}

	tests := []struct {
		path, want, notWant, poll string
	}{
		{"/dashboard", "Active one", "Closed one", `hx-get="/dashboard"`},
		{"/dashboard?view=active", "Active one", "Closed one", `hx-get="/dashboard"`},
		{"/dashboard?view=closed", "Closed one", "Active one", `hx-get="/dashboard?view=closed"`},
		{"/dashboard?view=bogus", "Active one", "Closed one", `hx-get="/dashboard"`},
	}
	for _, tc := range tests {
		t.Run(tc.path, func(t *testing.T) {
			body := get(tc.path)
			if !strings.Contains(body, tc.want) {
				t.Errorf("missing %q", tc.want)
			}
			if strings.Contains(body, tc.notWant) {
				t.Errorf("unexpectedly contains %q", tc.notWant)
			}
			if !strings.Contains(body, tc.poll) {
				t.Errorf("poll attribute %s not found", tc.poll)
			}
			for _, link := range []string{`href="/dashboard?view=active"`, `href="/dashboard?view=closed"`} {
				if !strings.Contains(body, link) {
					t.Errorf("toggle link %s not found", link)
				}
			}
		})
	}
}
