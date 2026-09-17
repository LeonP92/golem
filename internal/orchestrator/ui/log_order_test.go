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

// The activity log reads newest-first. Two things have to agree for that to
// hold: the entries rendered with the page, and the live ones htmx swaps in
// over SSE. If the initial order were reversed without changing the swap from
// beforeend to afterbegin, new entries would pile up at the BOTTOM, below the
// oldest — the worst of both orders.
func TestActivityLogIsNewestFirst(t *testing.T) {
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

	// Ordered by sequence_num, and seeded out of order so the test cannot
	// pass by accident of insertion order.
	for _, e := range []db.LogEntry{
		{TicketID: ticket.ID, SequenceNum: 2, EntryType: "STATUS", FromRole: "shem", Message: "SECOND-ENTRY"},
		{TicketID: ticket.ID, SequenceNum: 1, EntryType: "STATUS", FromRole: "human", Message: "FIRST-ENTRY"},
		{TicketID: ticket.ID, SequenceNum: 3, EntryType: "STATUS", FromRole: "shem", Message: "THIRD-ENTRY"},
		// A SPEC document is pulled out of the stream entirely; the scan that
		// does that relies on ascending order, so it is seeded here to prove
		// the reversal did not disturb it.
		{TicketID: ticket.ID, SequenceNum: 4, EntryType: "SPEC", FromRole: "shem", Message: "THE-SPEC"},
	} {
		if err := gdb.Create(&e).Error; err != nil {
			t.Fatalf("seed entry: %v", err)
		}
	}

	tmpls, err := ui.LoadTemplates()
	if err != nil {
		t.Fatalf("LoadTemplates: %v", err)
	}
	rec := httptest.NewRecorder()
	if err := auth.CreateSession(gdb, rec, user.ID, false); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	mux := http.NewServeMux()
	ui.NewHandlersWithMap(gdb, tmpls, false).RegisterRoutes(mux)

	req := httptest.NewRequest(http.MethodGet, "/tickets/"+ticket.ID, nil)
	req.AddCookie(rec.Result().Cookies()[0])
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("GET the ticket page = %d: %s", w.Code, w.Body.String())
	}
	body := w.Body.String()

	third := strings.Index(body, "THIRD-ENTRY")
	second := strings.Index(body, "SECOND-ENTRY")
	first := strings.Index(body, "FIRST-ENTRY")
	if third == -1 || second == -1 || first == -1 {
		t.Fatalf("an entry is missing from the page (third=%d second=%d first=%d)", third, second, first)
	}
	if !(third < second && second < first) {
		t.Errorf("entries render oldest-first: positions third=%d second=%d first=%d", third, second, first)
	}

	// The live swap has to match the rendered order.
	feed := body[strings.Index(body, `id="log-feed"`):]
	feed = feed[:strings.Index(feed, ">")]
	if !strings.Contains(feed, `hx-swap="afterbegin"`) {
		t.Error(`#log-feed does not swap afterbegin: live entries would append below the oldest`)
	}

	// And the SPEC is still lifted out of the stream, which depends on the
	// ascending scan the reversal deliberately left alone.
	if strings.Count(body, "THE-SPEC") == 0 {
		t.Error("the SPEC document vanished from the page")
	}
}
