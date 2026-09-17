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

// The ticket page has two independent live mechanisms, and they have to agree.
//
// The activity log streams over SSE. Everything else — the phase badge, the
// action buttons, the Details block — comes from the server-rendered page and
// was never refreshed, so a ticket could sit on screen advertising a phase it
// had already left. #log-feed already carried hx-preserve, which only means
// anything if its container is swapped, so the polling was intended and
// missing rather than deliberately absent.
//
// The invariant worth pinning is the interaction: polling a container that
// holds an SSE-connected element only works while that element is preserved.
// Without hx-preserve the swap would replace #log-feed every few seconds,
// dropping and reopening the event stream and losing the scroll position.
func TestTicketPagePollsWithoutDroppingTheLogStream(t *testing.T) {
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
	attrs := live[1]
	for _, want := range []string{`hx-get="/tickets/` + ticket.ID + `"`, `hx-trigger="every`, `hx-select="#ticket-live"`} {
		if !strings.Contains(attrs, want) {
			t.Errorf("#ticket-live is missing %s — the phase and action buttons will not follow the ticket", want)
		}
	}

	feed := regexp.MustCompile(`(?s)<div id="log-feed"(.*?)>`).FindStringSubmatch(body)
	if feed == nil {
		t.Fatal("no #log-feed on the ticket page")
	}
	if !strings.Contains(feed[1], "hx-preserve") {
		t.Error("#log-feed has no hx-preserve: each poll of its container would drop and reopen the SSE stream")
	}
	if !strings.Contains(feed[1], "sse-connect=") {
		t.Error("#log-feed no longer connects to SSE; the activity log would only update on a poll")
	}
}
