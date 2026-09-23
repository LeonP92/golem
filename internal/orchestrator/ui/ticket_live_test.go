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

// The activity log is the only live part of the ticket page; it streams over
// a WebSocket. The rest of the page must NOT be polled: #ticket-live holds the
// Request Changes form, and a timed swap of it replaced the textarea every few
// seconds, discarding whatever the operator was in the middle of typing.
func TestTicketPageStreamsOnlyTheLog(t *testing.T) {
	gdb, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	user := db.User{Username: "leon", PasswordHash: "x", Role: string(rbac.RoleAdmin)}
	if err := gdb.Create(&user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	ticket := db.Ticket{
		ID: "29a47f29-18de-427e-b276-7e9039539889", RepoRemote: "https://github.com/acme/widgets",
		Branch: "ticket/x", Description: "d", Phase: "implement",
	}
	if err := gdb.Create(&ticket).Error; err != nil {
		t.Fatalf("create ticket: %v", err)
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

	req := httptest.NewRequest(http.MethodGet, "/tickets/"+ticket.ID, nil)
	req.AddCookie(cookie)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("GET the ticket page = %d: %s", w.Code, w.Body.String())
	}
	body := w.Body.String()

	live := regexp.MustCompile(`(?s)<div id="ticket-live"(.*?)>`).FindStringSubmatch(body)
	if live == nil {
		t.Fatal("no #ticket-live container on the ticket page")
	}
	if strings.Contains(live[1], "hx-trigger") || strings.Contains(live[1], "hx-get") {
		t.Error("#ticket-live is polled again: each swap wipes text typed into the Request Changes form")
	}

	feed := regexp.MustCompile(`(?s)<div id="log-feed"(.*?)>`).FindStringSubmatch(body)
	if feed == nil {
		t.Fatal("no #log-feed on the ticket page")
	}
	if !strings.Contains(feed[1], `data-log-ws="/ws/tickets/`+ticket.ID+`/log"`) {
		t.Error("#log-feed carries no data-log-ws endpoint; the activity log would only update on a poll")
	}

	// The feed must NOT go back to SSE. It held one HTTP connection open per
	// ticket tab, and browsers allow six per origin on HTTP/1.1, so six open
	// tabs consumed the entire pool and every subsequent request — including
	// an ordinary page navigation — queued behind them and never completed.
	// The whole UI stopped loading while the server stayed perfectly healthy,
	// which is why this is pinned rather than left to review.
	if strings.Contains(feed[1], "sse-connect=") || strings.Contains(body, "htmx-ext-sse") {
		t.Error("the activity log is back on SSE: six open ticket tabs will exhaust " +
			"the browser's six-connection-per-origin limit and wedge the entire UI")
	}
}
