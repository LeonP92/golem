package ui_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/leonp92/golem/internal/orchestrator/auth"
	"github.com/leonp92/golem/internal/orchestrator/db"
	"github.com/leonp92/golem/internal/orchestrator/ghsync"
	"github.com/leonp92/golem/internal/orchestrator/ui"
)

// setupParkedTest returns a mux with the UI routes and a session cookie,
// plus a repo, a linked ticket, and one parked outbox row.
func setupParkedTest(t *testing.T) (*ui.Handlers, *http.ServeMux, *http.Cookie, db.GitHubOutbox) {
	t.Helper()
	gdb, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	repo := db.GitHubRepo{RepoRemote: "https://github.com/org/repo",
		Owner: "org", Name: "repo", Enabled: true, Label: "golem"}
	if err := gdb.Create(&repo).Error; err != nil {
		t.Fatalf("seed repo: %v", err)
	}
	n := 7
	ticket := db.Ticket{ID: "t1", RepoRemote: repo.RepoRemote, Title: "Add rate limiting",
		Branch: "b", Description: "d", Phase: "ready-for-review", IssueNumber: &n}
	if err := gdb.Create(&ticket).Error; err != nil {
		t.Fatalf("seed ticket: %v", err)
	}
	row := db.GitHubOutbox{
		TicketID: "t1", Kind: ghsync.KindPR, Payload: "{}",
		IdempotencyKey: ghsync.PRKey("t1"),
		Attempts:       ghsync.MaxAttempts,
		NextAttempt:    time.Now().Add(30 * time.Minute),
		LastError:      `create pull request: 422 <img src=x onerror=alert(1)>`,
	}
	if err := gdb.Create(&row).Error; err != nil {
		t.Fatalf("seed outbox row: %v", err)
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
	return h, mux, cookie, row
}

// TestGitHubSettingsShowsParkedOutboxRows covers finding I6. publish.go
// claimed a parked row was "surfaced in the dashboard" and "visible in the
// dashboard through LastError"; it was not — GitHubOutbox.LastError appeared
// in no template, handler or CLI. A parked comment or pull-request write was
// lost permanently behind a single log line, and a parked row also makes
// hasParkedOutboxRows true forever, so every later 304 poll pays a full
// reconcile for the life of the deployment with no way to clear it.
func TestGitHubSettingsShowsParkedOutboxRows(t *testing.T) {
	_, mux, cookie, row := setupParkedTest(t)

	req := httptest.NewRequest(http.MethodGet, "/settings/github", nil)
	withSession(req, cookie)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	body := w.Body.String()

	for _, want := range []string{"Add rate limiting", ghsync.KindPR, "422"} {
		if !strings.Contains(body, want) {
			t.Errorf("settings page does not mention %q — a parked row is invisible to the operator", want)
		}
	}
	if !strings.Contains(body, fmt.Sprintf("/settings/github/outbox/%d/retry", row.ID)) {
		t.Error("settings page offers no way to retry a parked row")
	}
	// LastError is raw text from the GitHub API, so it must be escaped like
	// every other untrusted value on this page.
	if strings.Contains(body, "<img src=x") {
		t.Error("parked row LastError rendered unescaped")
	}
}

// TestRetryParkedOutboxRow asserts the operator action actually un-parks the
// row: attempts reset to 0 and next_attempt brought forward, so the next
// drain pass picks it up.
func TestRetryParkedOutboxRow(t *testing.T) {
	h, mux, cookie, row := setupParkedTest(t)

	req := httptest.NewRequest(http.MethodPost,
		fmt.Sprintf("/settings/github/outbox/%d/retry", row.ID), nil)
	withSession(req, cookie)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303: %s", w.Code, w.Body.String())
	}

	var got db.GitHubOutbox
	if err := h.DB.First(&got, row.ID).Error; err != nil {
		t.Fatalf("reload row: %v", err)
	}
	if got.Attempts != 0 {
		t.Errorf("attempts = %d, want 0", got.Attempts)
	}
	if got.NextAttempt.After(time.Now()) {
		t.Errorf("next_attempt = %v, want due now or earlier", got.NextAttempt)
	}
	if got.DoneAt != nil {
		t.Error("retry must not mark the row done")
	}
}

// TestRetryParkedOutboxRowRequiresCSRF keeps the new state-changing POST in
// line with every other session-authenticated POST (round 1a, finding S1).
func TestRetryParkedOutboxRowRequiresCSRF(t *testing.T) {
	h, mux, cookie, row := setupParkedTest(t)

	req := httptest.NewRequest(http.MethodPost,
		fmt.Sprintf("/settings/github/outbox/%d/retry", row.ID), nil)
	req.AddCookie(cookie) // no CSRF token
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403 without a CSRF token", w.Code)
	}

	var got db.GitHubOutbox
	h.DB.First(&got, row.ID)
	if got.Attempts != ghsync.MaxAttempts {
		t.Errorf("attempts = %d, want %d — the row must not be un-parked without a token",
			got.Attempts, ghsync.MaxAttempts)
	}
}
