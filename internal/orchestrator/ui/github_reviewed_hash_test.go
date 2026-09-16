package ui_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/leonp92/golem/internal/orchestrator/auth"
	"github.com/leonp92/golem/internal/orchestrator/db"
	"github.com/leonp92/golem/internal/orchestrator/ghsync"
	"github.com/leonp92/golem/internal/orchestrator/ui"
)

// TestTicketDetailSubmitsTheHashItRendered is the page half of fix round 1c.
// actionStart now refuses an approval that does not carry the body_hash the
// operator's page was rendered from, so the page must carry it — otherwise
// the only control that can approve a ticket is one the server rejects.
//
// It asserts the hash is the one matching the description the page actually
// displays, not merely that some hash is present: a control wired to a
// constant, or to a hash computed from anything other than the rendered text,
// would satisfy "a field exists" while defeating the check entirely.
func TestTicketDetailSubmitsTheHashItRendered(t *testing.T) {
	gdb, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	const description = "the description a human is about to read"
	number := 11
	ticket := db.Ticket{
		ID: "reviewed-hash-ticket", RepoRemote: "https://github.com/org/repo",
		Title: "t", Branch: "ticket/x", Description: description,
		BodyHash: ghsync.HashBody(description), Phase: "pending-approval",
		IssueNumber: &number, IssueURL: "https://github.com/org/repo/issues/11",
	}
	if err := gdb.Create(&ticket).Error; err != nil {
		t.Fatalf("seed ticket: %v", err)
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
	h := ui.NewHandlersWithMap(gdb, tmpls, false)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	req := httptest.NewRequest(http.MethodGet, "/tickets/"+ticket.ID, nil)
	withSession(req, cookie)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	body := w.Body.String()

	if !strings.Contains(body, description) {
		t.Fatal("the page does not render the description it is asking the operator to approve")
	}
	// hx-vals sits in a single-quoted attribute, so html/template leaves the
	// JSON's own double quotes alone and escapes only the interpolated value
	// — which for a hex hash is a no-op.
	want := `"reviewed_body_hash":"` + ghsync.HashBody(description) + `"`
	if !strings.Contains(body, want) {
		t.Errorf("approval control does not submit the hash of the description it rendered; want %s", want)
	}
}
