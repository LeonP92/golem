package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"

	"github.com/leonp92/golem/internal/github"
	"github.com/leonp92/golem/internal/orchestrator/api"
	"github.com/leonp92/golem/internal/orchestrator/auth"
	"github.com/leonp92/golem/internal/orchestrator/db"
	"github.com/leonp92/golem/internal/orchestrator/ghsync"
	"github.com/leonp92/golem/internal/orchestrator/sse"
	ws "github.com/leonp92/golem/internal/orchestrator/ws"
)

// openTestDB opens an in-memory SQLite database with a single connection so all
// goroutines share the same in-memory state (multiple open connections each get
// their own empty in-memory database).
func openTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	gdb, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	sqlDB, err := gdb.DB()
	if err != nil {
		t.Fatalf("gdb.DB: %v", err)
	}
	sqlDB.SetMaxOpenConns(1)
	return gdb
}

// seedTestShem creates a Shem row with a bcrypt-hashed API key and returns
// both the row and the plain-text key.
func seedTestShem(t *testing.T, gdb *gorm.DB, name, plainKey string) db.Shem {
	t.Helper()
	hash, err := bcrypt.GenerateFromPassword([]byte(plainKey), bcrypt.MinCost)
	if err != nil {
		t.Fatalf("bcrypt: %v", err)
	}
	shem := db.Shem{Name: name, APIKeyHash: string(hash), Repos: `["https://github.com/org/repo"]`, Status: "online"}
	if err := gdb.Create(&shem).Error; err != nil {
		t.Fatalf("create shem: %v", err)
	}
	return shem
}

// TestConcurrentClaim_ExactlyOneWinner fires 20 goroutines all trying to claim
// the same unassigned ticket. Exactly one must succeed; all others must fail.
func TestConcurrentClaim_ExactlyOneWinner(t *testing.T) {
	gdb := openTestDB(t)

	// Seed shems (one per goroutine so each has a valid ID).
	const n = 20
	shems := make([]db.Shem, n)
	for i := range shems {
		shems[i] = seedTestShem(t, gdb, fmt.Sprintf("shem-%d", i), fmt.Sprintf("key-%d", i))
	}

	ticket := db.Ticket{
		RepoRemote:  "https://github.com/org/repo",
		Branch:      "ticket/concurrent",
		Description: "concurrent claim test",
		Phase:       "unassigned",
	}
	if err := gdb.Create(&ticket).Error; err != nil {
		t.Fatalf("create ticket: %v", err)
	}

	h := api.NewHandlers(gdb, ws.NewHub(), sse.NewBroker())

	wins := make([]bool, n)
	var wg sync.WaitGroup
	for i := range wins {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			_, err := h.ClaimTicket(ticket.ID, shems[idx].ID)
			wins[idx] = err == nil
		}(i)
	}
	wg.Wait()

	count := 0
	for _, w := range wins {
		if w {
			count++
		}
	}
	if count != 1 {
		t.Errorf("expected exactly 1 winner, got %d", count)
	}

	// Confirm DB state.
	var got db.Ticket
	gdb.First(&got, "id = ?", ticket.ID)
	if got.Phase != "claimed" {
		t.Errorf("expected phase=claimed, got %q", got.Phase)
	}
	if got.AssignedShem == nil {
		t.Error("expected assigned_shem to be set")
	}
}

// TestHeartbeatMonitor_RequeuesAndNewShemClaims simulates a dead Shem-A that
// held a ticket. The heartbeat monitor should mark it offline and requeue the
// ticket. Shem-B can then claim it.
func TestHeartbeatMonitor_RequeuesAndNewShemClaims(t *testing.T) {
	gdb := openTestDB(t)

	old := time.Now().Add(-3 * time.Minute)
	deadShem := db.Shem{
		Name:          "dead-shem",
		APIKeyHash:    "x",
		Repos:         `["https://github.com/org/repo"]`,
		Status:        "online",
		LastHeartbeat: &old,
	}
	if err := gdb.Create(&deadShem).Error; err != nil {
		t.Fatalf("create dead shem: %v", err)
	}

	ticket := db.Ticket{
		RepoRemote:   "https://github.com/org/repo",
		Branch:       "ticket/recovery",
		Description:  "recovery test",
		Phase:        "implement",
		AssignedShem: &deadShem.ID,
	}
	if err := gdb.Create(&ticket).Error; err != nil {
		t.Fatalf("create ticket: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	hub := ws.NewHub()
	// checkInterval=50ms, timeout=60s (shem is 3min stale → already dead).
	ws.StartHeartbeatMonitor(ctx, gdb, hub, 50*time.Millisecond, 60*time.Second)
	time.Sleep(200 * time.Millisecond)

	// Ticket should now be unassigned.
	var got db.Ticket
	gdb.First(&got, "id = ?", ticket.ID)
	if got.Phase != "unassigned" {
		t.Fatalf("expected unassigned after shem death, got %q", got.Phase)
	}
	if got.AssignedShem != nil {
		t.Error("expected assigned_shem to be nil after requeue")
	}

	// Shem-B can now claim the recovered ticket.
	liveShem := seedTestShem(t, gdb, "live-shem", "livekey")
	h := api.NewHandlers(gdb, hub, sse.NewBroker())
	claim, err := h.ClaimTicket(ticket.ID, liveShem.ID)
	if err != nil {
		t.Fatalf("live shem could not claim recovered ticket: %v", err)
	}
	if claim.TicketID != ticket.ID {
		t.Errorf("expected ticket_id=%s, got %s", ticket.ID, claim.TicketID)
	}
	// No checkpoint was posted by the dead shem, so CheckpointPhase must be nil.
	if claim.CheckpointPhase != nil {
		t.Errorf("expected nil CheckpointPhase (fresh ticket), got %v", claim.CheckpointPhase)
	}
}

// TestLogForwarding_SSEDeliversInSequenceOrder posts three log entries via the
// HTTP endpoint and asserts the SSE broker delivers them in sequence-number
// order to a direct subscriber.
func TestLogForwarding_SSEDeliversInSequenceOrder(t *testing.T) {
	gdb := openTestDB(t)

	shem := seedTestShem(t, gdb, "log-shem", "logkey")
	ticket := db.Ticket{
		RepoRemote:   "https://github.com/org/repo",
		Branch:       "ticket/log-forward",
		Description:  "log forwarding test",
		Phase:        "implement",
		AssignedShem: &shem.ID,
	}
	if err := gdb.Create(&ticket).Error; err != nil {
		t.Fatalf("create ticket: %v", err)
	}

	broker := sse.NewBroker()
	h := api.NewHandlers(gdb, ws.NewHub(), broker)
	mux := http.NewServeMux()
	h.RegisterLogRoutes(mux)

	// Subscribe before posting so we don't miss any events.
	ch, cancel := broker.Subscribe(ticket.ID)
	defer cancel()

	messages := []string{"alpha", "beta", "gamma"}
	for _, msg := range messages {
		body, _ := json.Marshal(map[string]string{
			"entry_type": "STATUS",
			"from_role":  "developer",
			"message":    msg,
		})
		url := fmt.Sprintf("/api/tickets/%s/log", ticket.ID)
		req := httptest.NewRequest(http.MethodPost, url, bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer logkey")
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("POST log %q: expected 200, got %d: %s", msg, w.Code, w.Body.String())
		}
	}

	// Drain the channel and verify sequence numbers are strictly ascending.
	received := make([]sse.LogEntryEvent, 0, len(messages))
	timeout := time.After(2 * time.Second)
	for len(received) < len(messages) {
		select {
		case evt := <-ch:
			received = append(received, evt)
		case <-timeout:
			t.Fatalf("timed out waiting for SSE events; received %d/%d", len(received), len(messages))
		}
	}

	for i, evt := range received {
		wantSeq := uint(i + 1)
		if evt.SequenceNum != wantSeq {
			t.Errorf("event[%d]: expected sequence_num=%d, got %d", i, wantSeq, evt.SequenceNum)
		}
		if evt.Message != messages[i] {
			t.Errorf("event[%d]: expected message=%q, got %q", i, messages[i], evt.Message)
		}
	}
}

// --- GitHub issue-to-ticket-to-comment end-to-end coverage -----------------
//
// The tests below exercise the full issue -> ticket -> phase -> GitHub write
// loop across package boundaries that no per-package unit test crosses:
// ghsync.Syncer creates and reconciles tickets, the real api.Handlers HTTP
// routes (not hand-built transactions) drive the intake-approval release and
// phase transitions, and ghsync.Syncer.Drain delivers the resulting outbox
// rows to a fake GitHub. Package-level tests in internal/orchestrator/api
// stop at "an outbox row was enqueued with the right payload"; package-level
// tests in internal/orchestrator/ghsync start from a hand-built outbox row.
// Nothing else proves the handoff between them actually works.
//
// githubTestEnv bundles the handlers, mux, and auth material shared by them.
type githubTestEnv struct {
	h      *api.Handlers
	mux    *http.ServeMux
	cookie *http.Cookie
	shem   db.Shem
	apiKey string
}

// newGitHubTestEnv wires an api.Handlers with the ticket, human-action, and
// GitHub routes registered against gdb, plus a session cookie (for the
// human-facing action dispatcher) and a shem API key (for the shem-facing
// claim/phase/branch-pushed routes).
func newGitHubTestEnv(t *testing.T, gdb *gorm.DB) *githubTestEnv {
	t.Helper()
	h := api.NewHandlers(gdb, ws.NewHub(), sse.NewBroker())
	mux := http.NewServeMux()
	h.RegisterHumanRoutes(mux)
	h.RegisterTicketRoutes(mux)
	h.RegisterGitHubRoutes(mux)

	user := db.User{Username: "gh-e2e-admin", PasswordHash: "x"}
	if err := gdb.Create(&user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	rec := httptest.NewRecorder()
	if err := auth.CreateSession(gdb, rec, user.ID, false); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	cookies := rec.Result().Cookies()
	if len(cookies) == 0 {
		t.Fatal("no session cookie returned")
	}

	const apiKey = "gh-e2e-key"
	shem := seedTestShem(t, gdb, "gh-e2e-shem", apiKey)
	return &githubTestEnv{h: h, mux: mux, cookie: cookies[0], shem: shem, apiKey: apiKey}
}

// doAction POSTs a human ticket action (session-authenticated).
func (e *githubTestEnv) doAction(t *testing.T, ticketID, action string) int {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"action": action})
	req := httptest.NewRequest(http.MethodPost, "/api/tickets/"+ticketID+"/actions", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(e.cookie)
	w := httptest.NewRecorder()
	e.mux.ServeHTTP(w, req)
	return w.Code
}

// doPhase PATCHes a ticket's phase (shem-authenticated).
func (e *githubTestEnv) doPhase(t *testing.T, ticketID, phase string) int {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"phase": phase})
	req := httptest.NewRequest(http.MethodPatch, "/api/tickets/"+ticketID+"/phase", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+e.apiKey)
	req.Header.Set("X-Shem-Name", e.shem.Name)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	e.mux.ServeHTTP(w, req)
	return w.Code
}

// doClaim POSTs a ticket claim attempt through the real HTTP endpoint
// (shem-authenticated) and returns the status code.
func (e *githubTestEnv) doClaim(t *testing.T, ticketID string) int {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/tickets/"+ticketID+"/claim", nil)
	req.Header.Set("Authorization", "Bearer "+e.apiKey)
	req.Header.Set("X-Shem-Name", e.shem.Name)
	w := httptest.NewRecorder()
	e.mux.ServeHTTP(w, req)
	return w.Code
}

// doBranchPushed POSTs the branch-pushed callback (shem-authenticated).
func (e *githubTestEnv) doBranchPushed(t *testing.T, ticketID string) int {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/tickets/"+ticketID+"/branch-pushed", bytes.NewReader(nil))
	req.Header.Set("Authorization", "Bearer "+e.apiKey)
	req.Header.Set("X-Shem-Name", e.shem.Name)
	w := httptest.NewRecorder()
	e.mux.ServeHTTP(w, req)
	return w.Code
}

// TestGitHubIssueToTicketToComment walks the whole loop against the fake: an
// issue appears, becomes a ticket, is released by a human past the intake
// approval gate, is claimed, advances phase, and the label and milestone
// comment are delivered.
//
// This supersedes the plan's original brief for this test, written before
// the intake-approval gate existed: a newly ingested, GitHub-sourced ticket
// now starts in "pending-approval" (spec Amendment 1) and is not claimable
// until a human releases it via the "start" action — see
// ghsync/ingest.go's createTicketFromIssue and api/human.go's actionStart.
// This test exercises that gate rather than assuming the old "straight to
// unassigned" behaviour.
func TestGitHubIssueToTicketToComment(t *testing.T) {
	gdb := openTestDB(t)

	repo := db.GitHubRepo{RepoRemote: "https://github.com/org/repo",
		Owner: "org", Name: "repo", Enabled: true, Label: "golem"}
	if err := gdb.Create(&repo).Error; err != nil {
		t.Fatalf("seed repo: %v", err)
	}

	f := github.NewFake()
	f.AddIssue(github.Issue{Number: 7, Title: "Add rate limiting",
		Body: "details", State: "open", UpdatedAt: time.Now(),
		HTMLURL: "https://github.com/org/repo/issues/7",
		Labels:  []string{"golem"}})

	s := ghsync.NewSyncer(gdb, f)
	ctx := context.Background()

	// Ingest creates the ticket, gated in pending-approval — not claimable
	// yet, regardless of what phase gymnastics later handlers can do.
	if err := s.IngestRepo(ctx, &repo); err != nil {
		t.Fatalf("IngestRepo: %v", err)
	}
	var ticket db.Ticket
	if err := gdb.First(&ticket, "issue_number = ?", 7).Error; err != nil {
		t.Fatalf("ticket not created: %v", err)
	}
	if ticket.Phase != "pending-approval" {
		t.Fatalf("phase = %q, want pending-approval", ticket.Phase)
	}
	if ticket.IntakeApproved {
		t.Fatal("a newly ingested ticket must not start approved")
	}

	env := newGitHubTestEnv(t, gdb)

	// The gate must actually refuse a claim before a human releases the
	// ticket — asserted through the real claim endpoint, not by reading the
	// phase column. Going through the gate later without ever trying to go
	// around it here would not distinguish an enforced gate from one that
	// merely sets a phase nobody checks.
	if code := env.doClaim(t, ticket.ID); code != http.StatusConflict {
		t.Fatalf("claim before start status = %d, want 409 — a pending-approval "+
			"ticket must not be claimable", code)
	}
	var stillGated db.Ticket
	if err := gdb.First(&stillGated, "id = ?", ticket.ID).Error; err != nil {
		t.Fatalf("reload ticket: %v", err)
	}
	if stillGated.Phase != "pending-approval" || stillGated.AssignedShem != nil {
		t.Fatalf("ticket mutated by a refused claim: phase=%q assigned_shem=%v",
			stillGated.Phase, stillGated.AssignedShem)
	}

	// A human releases the ticket through the real HTTP action dispatcher —
	// the only writer of IntakeApproved.
	if code := env.doAction(t, ticket.ID, "start"); code != http.StatusNoContent {
		t.Fatalf("start action status = %d, want 204", code)
	}
	if err := gdb.First(&ticket, "id = ?", ticket.ID).Error; err != nil {
		t.Fatalf("reload ticket: %v", err)
	}
	if ticket.Phase != "unassigned" {
		t.Fatalf("phase after start = %q, want unassigned", ticket.Phase)
	}
	if !ticket.IntakeApproved || ticket.ApprovedBodyHash != ticket.BodyHash {
		t.Fatal("start must approve the ticket against its current body hash")
	}

	// Only now — released and approved — can a shem claim it.
	if _, err := env.h.ClaimTicket(ticket.ID, env.shem.ID); err != nil {
		t.Fatalf("ClaimTicket: %v", err)
	}

	// A phase advance, through the real handler, queues the label and the
	// milestone comment.
	if code := env.doPhase(t, ticket.ID, "implement"); code != http.StatusNoContent {
		t.Fatalf("phase update status = %d, want 204", code)
	}

	if err := s.Drain(ctx); err != nil {
		t.Fatalf("Drain: %v", err)
	}

	issue, err := f.GetIssue(ctx, "org", "repo", 7)
	if err != nil {
		t.Fatalf("GetIssue: %v", err)
	}
	if !issue.HasLabel("golem:implement") {
		t.Errorf("labels = %v, want golem:implement", issue.Labels)
	}
	if got := f.CommentsFor(7); len(got) != 1 {
		t.Fatalf("comments = %v, want exactly one", got)
	}

	// Draining again must not duplicate anything.
	if err := s.Drain(ctx); err != nil {
		t.Fatalf("second Drain: %v", err)
	}
	if got := f.CommentsFor(7); len(got) != 1 {
		t.Errorf("comment count = %d after a second drain, want 1", len(got))
	}
}

// outageClient wraps github.Fake so a test can model a GitHub outage that
// spans more than one delivery. github.Fake's own FailNext deliberately
// fails only the single next call before clearing itself — it cannot
// represent an outage lasting across the several GitHub calls a Drain pass
// (or several passes) makes while GitHub is unreachable, which is exactly
// what TestGitHubOutageDoesNotLosePhaseTransitions needs to hold steady
// across two separate phase transitions. Embedding *github.Fake satisfies
// github.Client for every method this test does not care about; the six
// overrides below are the ones Drain's deliver() can reach.
type outageClient struct {
	*github.Fake
	down bool
}

var errOutage = errors.New("dial tcp: connection refused")

func (c *outageClient) GetIssue(ctx context.Context, owner, repo string, number int) (github.Issue, error) {
	if c.down {
		return github.Issue{}, errOutage
	}
	return c.Fake.GetIssue(ctx, owner, repo, number)
}

func (c *outageClient) AddLabel(ctx context.Context, owner, repo string, number int, label string) error {
	if c.down {
		return errOutage
	}
	return c.Fake.AddLabel(ctx, owner, repo, number, label)
}

func (c *outageClient) RemoveLabel(ctx context.Context, owner, repo string, number int, label string) error {
	if c.down {
		return errOutage
	}
	return c.Fake.RemoveLabel(ctx, owner, repo, number, label)
}

func (c *outageClient) CreateComment(ctx context.Context, owner, repo string, number int, body string) error {
	if c.down {
		return errOutage
	}
	return c.Fake.CreateComment(ctx, owner, repo, number, body)
}

func (c *outageClient) SetIssueState(ctx context.Context, owner, repo string, number int, state string) error {
	if c.down {
		return errOutage
	}
	return c.Fake.SetIssueState(ctx, owner, repo, number, state)
}

func (c *outageClient) CreatePullRequest(ctx context.Context, owner, repo, head, base, title, body string, draft bool) (github.PullRequest, error) {
	if c.down {
		return github.PullRequest{}, errOutage
	}
	return c.Fake.CreatePullRequest(ctx, owner, repo, head, base, title, body, draft)
}

// TestGitHubOutageDoesNotLosePhaseTransitions proves that a GitHub outage
// loses no work: phase transitions keep committing locally, through the real
// HTTP handler, while GitHub is unreachable; the label and milestone-comment
// writes they queue accumulate rather than being dropped or retried away;
// and every one of them lands on the fake exactly once once GitHub recovers.
func TestGitHubOutageDoesNotLosePhaseTransitions(t *testing.T) {
	gdb := openTestDB(t)
	repo := db.GitHubRepo{RepoRemote: "https://github.com/org/repo",
		Owner: "org", Name: "repo", Enabled: true, Label: "golem"}
	if err := gdb.Create(&repo).Error; err != nil {
		t.Fatalf("seed repo: %v", err)
	}

	env := newGitHubTestEnv(t, gdb)
	n := 7
	shemID := env.shem.ID
	ticket := db.Ticket{ID: "t1", RepoRemote: repo.RepoRemote, Title: "t",
		Branch: "ticket/t-t1", BaseBranch: "main", Description: "d",
		Phase: "claimed", IssueNumber: &n, AssignedShem: &shemID, IntakeApproved: true}
	if err := gdb.Create(&ticket).Error; err != nil {
		t.Fatalf("seed ticket: %v", err)
	}

	client := &outageClient{Fake: github.NewFake()}
	client.AddIssue(github.Issue{Number: 7, State: "open", Labels: []string{"golem"}})
	s := ghsync.NewSyncer(gdb, client)
	ctx := context.Background()

	// Two phase transitions happen while GitHub is unreachable — the
	// ticket's lifecycle does not stall on GitHub's availability.
	client.down = true
	for _, phase := range []string{"plan", "implement"} {
		if code := env.doPhase(t, ticket.ID, phase); code != http.StatusNoContent {
			t.Fatalf("phase update to %s status = %d, want 204", phase, code)
		}
		if err := s.Drain(ctx); err != nil {
			t.Fatalf("Drain during outage (%s): %v", phase, err)
		}
	}
	if got := client.CommentsFor(7); len(got) != 0 {
		t.Fatalf("comment delivered despite the outage: %v", got)
	}

	var afterOutage db.Ticket
	if err := gdb.First(&afterOutage, "id = ?", ticket.ID).Error; err != nil {
		t.Fatalf("reload ticket: %v", err)
	}
	if afterOutage.Phase != "implement" {
		t.Fatalf("ticket phase = %q, want implement — a GitHub outage must not stall phase transitions", afterOutage.Phase)
	}

	var pending int64
	if err := gdb.Model(&db.GitHubOutbox{}).
		Where("ticket_id = ? AND done_at IS NULL", ticket.ID).Count(&pending).Error; err != nil {
		t.Fatalf("count pending rows: %v", err)
	}
	if pending != 4 { // label + milestone comment for each of "plan" and "implement"
		t.Fatalf("pending outbox rows = %d, want 4 — nothing should be lost", pending)
	}

	// Recovery: GitHub comes back, and the accumulated backlog's backoff is
	// force-expired (simulating that enough time has passed).
	client.down = false
	if err := gdb.Model(&db.GitHubOutbox{}).
		Where("ticket_id = ? AND done_at IS NULL", ticket.ID).
		Update("next_attempt", time.Now().Add(-time.Hour)).Error; err != nil {
		t.Fatalf("reset backoff: %v", err)
	}
	if err := s.Drain(ctx); err != nil {
		t.Fatalf("Drain after recovery: %v", err)
	}

	if got := client.CommentsFor(7); len(got) != 2 {
		t.Fatalf("comments = %v, want both queued comments delivered on recovery", got)
	}
	issue, err := client.GetIssue(ctx, "org", "repo", 7)
	if err != nil {
		t.Fatalf("GetIssue: %v", err)
	}
	if !issue.HasLabel("golem:implement") {
		t.Errorf("labels = %v, want golem:implement", issue.Labels)
	}

	var done int64
	if err := gdb.Model(&db.GitHubOutbox{}).
		Where("ticket_id = ? AND done_at IS NOT NULL", ticket.ID).Count(&done).Error; err != nil {
		t.Fatalf("count done rows: %v", err)
	}
	if done != 4 {
		t.Fatalf("done outbox rows = %d, want 4", done)
	}

	// Draining again after recovery must not duplicate anything.
	if err := s.Drain(ctx); err != nil {
		t.Fatalf("second Drain after recovery: %v", err)
	}
	if got := client.CommentsFor(7); len(got) != 2 {
		t.Errorf("comment count = %d after a second post-recovery drain, want 2", len(got))
	}
}

// TestReadyForReviewWithPushedBranchDeliversPRExactlyOnce closes the one gap
// neither package's own tests cover for the PR path: internal/orchestrator/api's
// TestPRQueuedWhenBothConditionsHold stops at "exactly one outbox row was
// queued"; internal/orchestrator/ghsync's TestDrainPullRequest starts from a
// hand-built row and never goes through the real handlers. This drives the
// real phase-update and branch-pushed HTTP endpoints and then drains to the
// fake, proving the queued row actually becomes one pull request — no more —
// with its number and URL recorded back onto the ticket.
func TestReadyForReviewWithPushedBranchDeliversPRExactlyOnce(t *testing.T) {
	gdb := openTestDB(t)
	repo := db.GitHubRepo{RepoRemote: "https://github.com/org/repo",
		Owner: "org", Name: "repo", Enabled: true, Label: "golem"}
	if err := gdb.Create(&repo).Error; err != nil {
		t.Fatalf("seed repo: %v", err)
	}

	env := newGitHubTestEnv(t, gdb)
	n := 7
	shemID := env.shem.ID
	ticket := db.Ticket{ID: "t2", RepoRemote: repo.RepoRemote, Title: "Add rate limiting",
		Branch: "ticket/add-rate-limiting-t2", BaseBranch: "main", Description: "d",
		Phase: "implement", IssueNumber: &n, AssignedShem: &shemID}
	if err := gdb.Create(&ticket).Error; err != nil {
		t.Fatalf("seed ticket: %v", err)
	}

	f := github.NewFake()
	f.AddIssue(github.Issue{Number: 7, State: "open", Labels: []string{"golem"}})
	s := ghsync.NewSyncer(gdb, f)
	ctx := context.Background()

	if code := env.doPhase(t, ticket.ID, "ready-for-review"); code != http.StatusNoContent {
		t.Fatalf("phase update status = %d, want 204", code)
	}
	if code := env.doBranchPushed(t, ticket.ID); code != http.StatusNoContent {
		t.Fatalf("branch-pushed status = %d, want 204", code)
	}

	if err := s.Drain(ctx); err != nil {
		t.Fatalf("Drain: %v", err)
	}
	prs := f.PRsSnapshot()
	if len(prs) != 1 {
		t.Fatalf("PRs created = %d, want exactly 1", len(prs))
	}

	var got db.Ticket
	if err := gdb.First(&got, "id = ?", ticket.ID).Error; err != nil {
		t.Fatalf("reload ticket: %v", err)
	}
	if got.PRNumber == nil || *got.PRNumber != prs[0].Number {
		t.Errorf("ticket.PRNumber = %v, want %d recorded from the created PR", got.PRNumber, prs[0].Number)
	}
	if got.PRURL == "" {
		t.Error("ticket.PRURL not recorded")
	}

	// Draining again must not open a second pull request.
	if err := s.Drain(ctx); err != nil {
		t.Fatalf("second Drain: %v", err)
	}
	if prs := f.PRsSnapshot(); len(prs) != 1 {
		t.Errorf("PRs after second drain = %d, want still 1", len(prs))
	}
}
