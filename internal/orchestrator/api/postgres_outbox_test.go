package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"

	"github.com/leonp92/golem/internal/github"
	"github.com/leonp92/golem/internal/orchestrator/api"
	"github.com/leonp92/golem/internal/orchestrator/db"
	"github.com/leonp92/golem/internal/orchestrator/ghsync"
	"github.com/leonp92/golem/internal/orchestrator/sse"
	ws "github.com/leonp92/golem/internal/orchestrator/ws"
)

// postgresDSNEnv names the environment variable that points these tests at a
// throwaway PostgreSQL cluster. Every other test in this repository runs on
// db.Open(":memory:"), which cannot see finding C1 at all: SQLite reports a
// unique violation without aborting the surrounding transaction, so the
// outbox's swallowed duplicate is harmless there and catastrophic on
// PostgreSQL. These tests are skipped unless the DSN is set.
//
//	initdb -D /tmp/pg -U golem --auth=trust
//	pg_ctl -D /tmp/pg -o "-p 55432 -k /tmp/pgsock" start
//	createdb -h 127.0.0.1 -p 55432 -U golem golemtest
//	GOLEM_TEST_POSTGRES_DSN="postgres://golem@127.0.0.1:55432/golemtest?sslmode=disable" \
//	    go test ./internal/orchestrator/api/ -run TestPostgres
const postgresDSNEnv = "GOLEM_TEST_POSTGRES_DSN"

// openPostgres opens the throwaway cluster and truncates every table, so each
// test starts from an empty database regardless of what ran before it.
func openPostgres(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := os.Getenv(postgresDSNEnv)
	if dsn == "" {
		t.Skipf("%s not set", postgresDSNEnv)
	}
	gdb, err := db.Open(dsn)
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	tables := []string{
		"git_hub_outboxes", "human_inputs", "log_entries",
		"tickets", "git_hub_repos", "shems", "sessions", "users",
	}
	for _, table := range tables {
		if err := gdb.Exec("TRUNCATE TABLE " + table + " RESTART IDENTITY CASCADE").Error; err != nil {
			t.Fatalf("truncate %s: %v", table, err)
		}
	}
	return gdb
}

// TestPostgresPhaseReEntryDoesNotPoisonTheTransaction covers finding C1 for
// the revise cycle. A ticket that re-enters a phase it has already visited
// re-derives an idempotency key the outbox already holds. On PostgreSQL the
// server raises 23505 and puts the caller's transaction into the aborted
// state, so swallowing the error lost the phase write: the handler answered
// 500 and the ticket stayed where it was, forever, on every retry.
//
// Verified against HEAD ff2c226 on PostgreSQL 18.3:
//
//	implement        -> ready-for-review : 204
//	ready-for-review -> implement        : 204
//	implement        -> ready-for-review : 500
//	final phase = "implement"
func TestPostgresPhaseReEntryDoesNotPoisonTheTransaction(t *testing.T) {
	gdb := openPostgres(t)
	h := api.NewHandlers(gdb, ws.NewHub(), sse.NewBroker())
	mux := http.NewServeMux()
	h.RegisterTicketRoutes(mux)

	hash, _ := bcrypt.GenerateFromPassword([]byte("pgkey"), bcrypt.MinCost)
	shem := db.Shem{Name: "pg-shem", APIKeyHash: string(hash), Repos: "[]", Status: "online"}
	if err := gdb.Create(&shem).Error; err != nil {
		t.Fatalf("seed shem: %v", err)
	}

	number := 77
	ticket := db.Ticket{
		RepoRemote: "https://github.com/org/repo", Title: "t", Branch: "b",
		BaseBranch: "main", Description: "d", Phase: "implement",
		AssignedShem: &shem.ID, IssueNumber: &number, IntakeApproved: true,
	}
	if err := gdb.Create(&ticket).Error; err != nil {
		t.Fatalf("seed ticket: %v", err)
	}

	patch := func(phase string) int {
		body, _ := json.Marshal(map[string]string{"phase": phase})
		req := httptest.NewRequest(http.MethodPatch,
			fmt.Sprintf("/api/tickets/%s/phase", ticket.ID), bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Shem-Name", "pg-shem")
		req.Header.Set("Authorization", "Bearer pgkey")
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		return w.Code
	}

	steps := []string{"ready-for-review", "implement", "ready-for-review"}
	for i, phase := range steps {
		if code := patch(phase); code != http.StatusNoContent {
			t.Fatalf("step %d (-> %s): status = %d, want 204", i, phase, code)
		}
	}

	var got db.Ticket
	if err := gdb.First(&got, "id = ?", ticket.ID).Error; err != nil {
		t.Fatalf("reload ticket: %v", err)
	}
	if got.Phase != "ready-for-review" {
		t.Errorf("final phase = %q, want ready-for-review", got.Phase)
	}

	var labels int64
	gdb.Model(&db.GitHubOutbox{}).
		Where("ticket_id = ? AND kind = ?", ticket.ID, ghsync.KindLabel).Count(&labels)
	if labels != int64(len(steps)) {
		t.Errorf("label rows = %d, want %d (one per transition)", labels, len(steps))
	}
}

// TestPostgresReGatedTicketCanBeApprovedAgain covers finding C1 for the flow
// the intake gate exists to support. An issue author who edits their own
// issue after approval re-gates the ticket; approving it a second time
// re-derives the same "<ticket>:label:unassigned" key the first approval
// already wrote. On PostgreSQL that 500ed on every attempt, leaving the
// ticket permanently unapprovable and unclaimable with no in-product
// recovery — actionStart is the only writer of intake_approved.
//
// Verified against HEAD ff2c226 on PostgreSQL 18.3: second approve 500,
// retry 500, phase still "pending-approval", intake_approved still false.
func TestPostgresReGatedTicketCanBeApprovedAgain(t *testing.T) {
	gdb := openPostgres(t)
	h := api.NewHandlers(gdb, ws.NewHub(), sse.NewBroker())
	mux := http.NewServeMux()
	h.RegisterHumanRoutes(mux)

	user := db.User{Username: "pgadmin", PasswordHash: "x"}
	if err := gdb.Create(&user).Error; err != nil {
		t.Fatalf("seed user: %v", err)
	}
	cookie := makeSessionCookie(t, h, user.ID)

	fake := github.NewFake()
	fake.AddIssue(github.Issue{
		Number: 77, Title: "an issue", Body: "original body", State: "open",
		Labels: []string{"golem"}, UpdatedAt: time.Now().Add(-time.Hour),
	})
	repo := db.GitHubRepo{
		RepoRemote: "https://github.com/org/repo", Owner: "org", Name: "repo",
		Enabled: true, Label: "golem",
	}
	if err := gdb.Create(&repo).Error; err != nil {
		t.Fatalf("seed repo: %v", err)
	}
	syncer := ghsync.NewSyncer(gdb, fake)
	ctx := context.Background()
	if err := syncer.IngestRepo(ctx, &repo); err != nil {
		t.Fatalf("first ingest: %v", err)
	}

	var ticket db.Ticket
	if err := gdb.First(&ticket, "issue_number = ?", 77).Error; err != nil {
		t.Fatalf("ingested ticket: %v", err)
	}
	if ticket.Phase != "pending-approval" {
		t.Fatalf("ingested phase = %q, want pending-approval", ticket.Phase)
	}

	// Each call re-reads body_hash first, the way the operator's page does
	// before rendering the approval control (fix round 1c). After the
	// re-gate that value has moved, which is exactly the point: the operator
	// reloads, reads the new text, and approves that.
	start := func() int {
		var shown db.Ticket
		if err := gdb.First(&shown, "id = ?", ticket.ID).Error; err != nil {
			t.Fatalf("read ticket as the page would: %v", err)
		}
		body, _ := json.Marshal(map[string]string{
			"action": "start", "reviewed_body_hash": shown.BodyHash,
		})
		req := httptest.NewRequest(http.MethodPost,
			fmt.Sprintf("/api/tickets/%s/actions", ticket.ID), bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		withSession(req, cookie)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		return w.Code
	}

	if code := start(); code != http.StatusNoContent {
		t.Fatalf("first approve = %d, want 204", code)
	}

	// The issue author edits the body. The next poll must re-gate.
	fake.AddIssue(github.Issue{
		Number: 77, Title: "an issue", Body: "edited body", State: "open",
		Labels: []string{"golem"}, UpdatedAt: time.Now(),
	})
	if err := syncer.IngestRepo(ctx, &repo); err != nil {
		t.Fatalf("second ingest: %v", err)
	}
	var regated db.Ticket
	if err := gdb.First(&regated, "id = ?", ticket.ID).Error; err != nil {
		t.Fatalf("reload re-gated ticket: %v", err)
	}
	if regated.Phase != "pending-approval" || regated.IntakeApproved {
		t.Fatalf("after edit: phase=%q intake_approved=%v, want pending-approval/false",
			regated.Phase, regated.IntakeApproved)
	}

	if code := start(); code != http.StatusNoContent {
		t.Fatalf("second approve = %d, want 204", code)
	}

	var final db.Ticket
	if err := gdb.First(&final, "id = ?", ticket.ID).Error; err != nil {
		t.Fatalf("reload approved ticket: %v", err)
	}
	if final.Phase != "unassigned" || !final.IntakeApproved {
		t.Errorf("after second approve: phase=%q intake_approved=%v, want unassigned/true",
			final.Phase, final.IntakeApproved)
	}
	// The approval must bind to the text the human just re-reviewed, not to
	// the text that was on the ticket before the edit (finding I5).
	if final.ApprovedBodyHash != final.BodyHash {
		t.Errorf("approved_body_hash=%q body_hash=%q — approval did not bind to the current text",
			final.ApprovedBodyHash, final.BodyHash)
	}
}
