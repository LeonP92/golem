package ghsync_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/leonp92/golem/internal/github"
	"github.com/leonp92/golem/internal/orchestrator/db"
	"github.com/leonp92/golem/internal/orchestrator/ghsync"
	"gorm.io/gorm"
)

// seedTicketOnly creates a ticket linked to issue #7, without creating a
// repo row — the caller seeds the repo itself via newRepo so it can hold on
// to the returned *db.GitHubRepo to pass into ReconcileRepo.
func seedTicketOnly(t *testing.T, gdb *gorm.DB, phase string) {
	t.Helper()
	tk := db.Ticket{ID: "t1", RepoRemote: "https://github.com/org/repo",
		Title: "t", Branch: "ticket/t-t1", BaseBranch: "main", Description: "d",
		Phase: phase, IssueNumber: intPtr(7)}
	if err := gdb.Create(&tk).Error; err != nil {
		t.Fatalf("seed ticket: %v", err)
	}
}

// TestReconcileAppliesPhaseLabel covers the core label-diffing behaviour:
// restoring a missing golem:<phase> label, replacing a stale one while
// leaving foreign labels untouched, and — the phase the original task brief
// never accounted for — applying golem:pending-approval for a ticket still
// waiting on a human to release it from intake.
func TestReconcileAppliesPhaseLabel(t *testing.T) {
	tests := []struct {
		name          string
		phase         string
		initialLabels []string
		wantPresent   []string
		wantAbsent    []string
	}{
		{
			name:          "restores a missing phase label",
			phase:         "implement",
			initialLabels: []string{"golem"},
			wantPresent:   []string{"golem:implement", "golem"},
		},
		{
			name:          "replaces a stale phase label without touching foreign labels",
			phase:         "implement",
			initialLabels: []string{"golem", "golem:plan", "bug"},
			wantPresent:   []string{"golem:implement", "golem", "bug"},
			wantAbsent:    []string{"golem:plan"},
		},
		{
			name:          "applies golem:pending-approval for a ticket still gated on human review",
			phase:         "pending-approval",
			initialLabels: []string{"golem"},
			wantPresent:   []string{"golem:pending-approval", "golem"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gdb, err := db.Open(":memory:")
			if err != nil {
				t.Fatalf("Open: %v", err)
			}
			repo := newRepo(t, gdb)
			seedTicketOnly(t, gdb, tt.phase)

			f := github.NewFake()
			f.AddIssue(github.Issue{Number: 7, State: "open", Labels: tt.initialLabels})

			s := ghsync.NewSyncer(gdb, f)
			if err := s.ReconcileRepo(context.Background(), repo); err != nil {
				t.Fatalf("ReconcileRepo: %v", err)
			}

			issue, ok := f.IssueByNumber(7)
			if !ok {
				t.Fatal("issue 7 vanished")
			}
			for _, want := range tt.wantPresent {
				if !issue.HasLabel(want) {
					t.Errorf("missing label %q; have %v", want, issue.Labels)
				}
			}
			for _, notWant := range tt.wantAbsent {
				if issue.HasLabel(notWant) {
					t.Errorf("unexpected label %q present; have %v", notWant, issue.Labels)
				}
			}
		})
	}
}

// TestReconcileClosesIssueForClosedTicket covers the other diffable axis:
// open/closed state. A closed ticket must close an open issue, and must
// leave an already-closed one alone (idempotent — no redundant
// SetIssueState call).
func TestReconcileClosesIssueForClosedTicket(t *testing.T) {
	tests := []struct {
		name             string
		initialState     string
		wantStateChanged bool
	}{
		{name: "open issue is closed to match the ticket", initialState: "open", wantStateChanged: true},
		{name: "already-closed issue is left alone", initialState: "closed", wantStateChanged: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gdb, err := db.Open(":memory:")
			if err != nil {
				t.Fatalf("Open: %v", err)
			}
			repo := newRepo(t, gdb)
			seedTicketOnly(t, gdb, "closed")

			f := github.NewFake()
			f.AddIssue(github.Issue{Number: 7, State: tt.initialState, Labels: []string{"golem"}})

			s := ghsync.NewSyncer(gdb, f)
			if err := s.ReconcileRepo(context.Background(), repo); err != nil {
				t.Fatalf("ReconcileRepo: %v", err)
			}

			issue, _ := f.GetIssue(context.Background(), "org", "repo", 7)
			if issue.State != "closed" {
				t.Errorf("issue state = %q, want closed", issue.State)
			}

			called := false
			for _, c := range f.CallsSnapshot() {
				if c == "SetIssueState" {
					called = true
				}
			}
			if called != tt.wantStateChanged {
				t.Errorf("SetIssueState called = %v, want %v", called, tt.wantStateChanged)
			}
		})
	}
}

// TestReconcileNeverSendsOneShotEvents guards risk area 1: comments and pull
// requests are outbox-owned one-shot events with no end state to diff
// against, and reconcile must never call CreateComment or
// CreatePullRequest — doing so on a real issue would post a duplicate.
func TestReconcileNeverSendsOneShotEvents(t *testing.T) {
	gdb, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	repo := newRepo(t, gdb)
	seedTicketOnly(t, gdb, "implement")

	f := github.NewFake()
	f.AddIssue(github.Issue{Number: 7, State: "open", Labels: []string{"golem", "golem:plan"}})

	s := ghsync.NewSyncer(gdb, f)
	if err := s.ReconcileRepo(context.Background(), repo); err != nil {
		t.Fatalf("ReconcileRepo: %v", err)
	}

	if got := f.CommentsFor(7); len(got) != 0 {
		t.Errorf("comments = %v, want none — comments are outbox-owned, not reconcilable", got)
	}
	if got := f.PRsSnapshot(); len(got) != 0 {
		t.Errorf("pull requests = %v, want none — PRs are outbox-owned, not reconcilable", got)
	}
	for _, c := range f.CallsSnapshot() {
		if c == "CreateComment" || c == "CreatePullRequest" {
			t.Errorf("reconcile called %s — comments and PRs must never be re-sent by reconcile", c)
		}
	}
}

// TestReconcileLogsLabelRemovalButKeepsReconciling covers risk area 2: the
// ingest list query is label-filtered, so reconcile is the only place a lost
// trigger label is observable. Losing it is not a cancel signal — the
// ticket keeps running, and its phase label is still reconciled normally.
func TestReconcileLogsLabelRemovalButKeepsReconciling(t *testing.T) {
	gdb, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	repo := newRepo(t, gdb)
	seedTicketOnly(t, gdb, "implement")

	f := github.NewFake()
	// Trigger label ("golem") gone entirely.
	f.AddIssue(github.Issue{Number: 7, State: "open", Labels: []string{}})

	s := ghsync.NewSyncer(gdb, f)
	if err := s.ReconcileRepo(context.Background(), repo); err != nil {
		t.Fatalf("ReconcileRepo: %v", err)
	}

	var got db.Ticket
	if err := gdb.First(&got, "id = ?", "t1").Error; err != nil {
		t.Fatalf("load ticket: %v", err)
	}
	if got.Phase != "implement" {
		t.Errorf("phase = %q, want implement — removing the trigger label must not cancel work", got.Phase)
	}

	issue, _ := f.GetIssue(context.Background(), "org", "repo", 7)
	if !issue.HasLabel("golem:implement") {
		t.Error("golem:implement not applied despite the missing trigger label — removal must not halt reconciliation")
	}
}

// TestReconcileNeverTouchesIntakeGateFields covers risk area 3: reconcile
// must never clear or set IntakeApproved, ApprovedBodyHash, or BodyHash, and
// must never move a ticket out of pending-approval — that gate belongs to
// the API handlers (actionStart) and ghsync.applyIssue's re-gate alone.
func TestReconcileNeverTouchesIntakeGateFields(t *testing.T) {
	gdb, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	repo := newRepo(t, gdb)

	bodyHash := ghsync.HashBody("some body")
	tk := db.Ticket{
		ID: "t1", RepoRemote: "https://github.com/org/repo",
		Title: "t", Branch: "ticket/t-t1", BaseBranch: "main",
		Description: "some body", Phase: "pending-approval",
		IssueNumber:      intPtr(7),
		IntakeApproved:   false,
		ApprovedBodyHash: "",
		BodyHash:         bodyHash,
	}
	if err := gdb.Create(&tk).Error; err != nil {
		t.Fatalf("seed ticket: %v", err)
	}

	f := github.NewFake()
	f.AddIssue(github.Issue{Number: 7, State: "open", Labels: []string{"golem"}})

	s := ghsync.NewSyncer(gdb, f)
	if err := s.ReconcileRepo(context.Background(), repo); err != nil {
		t.Fatalf("ReconcileRepo: %v", err)
	}

	var got db.Ticket
	if err := gdb.First(&got, "id = ?", "t1").Error; err != nil {
		t.Fatalf("load ticket: %v", err)
	}
	if got.Phase != "pending-approval" {
		t.Errorf("phase = %q, want pending-approval — reconcile must never release a gated ticket", got.Phase)
	}
	if got.IntakeApproved {
		t.Error("IntakeApproved = true — reconcile must never touch the intake gate")
	}
	if got.ApprovedBodyHash != "" {
		t.Errorf("ApprovedBodyHash = %q, want empty — reconcile must never touch it", got.ApprovedBodyHash)
	}
	if got.BodyHash != bodyHash {
		t.Errorf("BodyHash = %q, want unchanged %q — reconcile must never touch it", got.BodyHash, bodyHash)
	}

	issue, _ := f.GetIssue(context.Background(), "org", "repo", 7)
	if !issue.HasLabel("golem:pending-approval") {
		t.Error("golem:pending-approval not applied to a gated ticket's issue")
	}
}

// TestReconcileTicketFailureDoesNotAbortRepo covers risk area 4: one
// ticket's issue lookup failing (here, an issue GitHub no longer has) must
// not stop the rest of the repo's tickets from being reconciled, and
// ReconcileRepo itself must still return nil.
func TestReconcileTicketFailureDoesNotAbortRepo(t *testing.T) {
	gdb, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	repo := newRepo(t, gdb)
	seedTicketOnly(t, gdb, "implement") // t1 -> issue #7, present in the fake

	missing := db.Ticket{ID: "t2", RepoRemote: "https://github.com/org/repo",
		Title: "gone", Branch: "ticket/gone-t2", BaseBranch: "main",
		Description: "d", Phase: "implement", IssueNumber: intPtr(99)}
	if err := gdb.Create(&missing).Error; err != nil {
		t.Fatalf("seed ticket: %v", err)
	}

	f := github.NewFake()
	f.AddIssue(github.Issue{Number: 7, State: "open", Labels: []string{"golem"}})
	// Issue #99 deliberately not seeded: GetIssue returns "not found".

	s := ghsync.NewSyncer(gdb, f)
	if err := s.ReconcileRepo(context.Background(), repo); err != nil {
		t.Fatalf("ReconcileRepo: want nil despite one ticket's issue lookup failing, got %v", err)
	}

	issue, _ := f.GetIssue(context.Background(), "org", "repo", 7)
	if !issue.HasLabel("golem:implement") {
		t.Error("the other ticket was not reconciled after an unrelated ticket's issue lookup failed")
	}
}

// TestReconcileWrapsQueryError confirms ReconcileRepo's own ticket query
// failure is wrapped and returned rather than swallowed, by dropping the
// tickets table out from under it.
func TestReconcileWrapsQueryError(t *testing.T) {
	gdb, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	repo := newRepo(t, gdb)
	if err := gdb.Exec("DROP TABLE tickets").Error; err != nil {
		t.Fatalf("drop table: %v", err)
	}

	s := ghsync.NewSyncer(gdb, github.NewFake())
	err = s.ReconcileRepo(context.Background(), repo)
	if err == nil {
		t.Fatal("ReconcileRepo: want error after tickets table dropped, got nil")
	}
	if !strings.Contains(err.Error(), "query linked tickets") {
		t.Errorf("error = %q, want it to mention %q", err.Error(), "query linked tickets")
	}
}

// TestIngestRepoAppliesReconcileAfterSuccess confirms IngestRepo's tail end
// actually wires ReconcileRepo in: a freshly ingested ticket has no other
// path that would ever apply golem:pending-approval to its issue —
// enqueueGitHubPhase is only reachable from the API handlers — so seeing
// that label after a bare IngestRepo call proves reconcile ran.
func TestIngestRepoAppliesReconcileAfterSuccess(t *testing.T) {
	gdb, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	repo := newRepo(t, gdb)
	f := github.NewFake()
	f.AddIssue(github.Issue{Number: 7, Title: "t", Body: "d", State: "open",
		UpdatedAt: time.Now(), Labels: []string{"golem"}})

	s := ghsync.NewSyncer(gdb, f)
	if err := s.IngestRepo(context.Background(), repo); err != nil {
		t.Fatalf("IngestRepo: %v", err)
	}

	issue, _ := f.GetIssue(context.Background(), "org", "repo", 7)
	if !issue.HasLabel("golem:pending-approval") {
		t.Error("IngestRepo did not reconcile the newly created ticket's phase label")
	}
}

// TestIngestRepoStillReconcilesAfterPartialFailure covers the interaction
// with the partial-failure guard directly: when one issue's ticket write
// fails, the page's cursor must still not advance, but the ticket that DID
// succeed must still be reconciled — one bad issue must not also disable
// reconciliation for the rest of the repo.
func TestIngestRepoStillReconcilesAfterPartialFailure(t *testing.T) {
	gdb, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := gdb.Exec(`
CREATE TRIGGER golem_test_fail_insert_reconcile
BEFORE INSERT ON tickets
WHEN NEW.title = 'boom-title'
BEGIN
    SELECT RAISE(ABORT, 'simulated write failure');
END;
`).Error; err != nil {
		t.Fatalf("create trigger: %v", err)
	}
	repo := newRepo(t, gdb)

	older := time.Now().Add(-time.Hour)
	newer := time.Now()
	f := github.NewFake()
	f.AddIssue(github.Issue{Number: 1, Title: "boom-title", State: "open",
		UpdatedAt: older, Labels: []string{"golem"}})
	f.AddIssue(github.Issue{Number: 2, Title: "ok-title", State: "open",
		UpdatedAt: newer, Labels: []string{"golem"}})

	s := ghsync.NewSyncer(gdb, f)
	if err := s.IngestRepo(context.Background(), repo); err != nil {
		t.Fatalf("IngestRepo: %v", err)
	}

	var got db.GitHubRepo
	if err := gdb.First(&got, repo.ID).Error; err != nil {
		t.Fatalf("reload repo: %v", err)
	}
	if got.LastIssueSync != nil {
		t.Errorf("LastIssueSync = %v, want nil — a partial failure must still not advance the cursor with reconcile wired in", *got.LastIssueSync)
	}

	issue, _ := f.GetIssue(context.Background(), "org", "repo", 2)
	if !issue.HasLabel("golem:pending-approval") {
		t.Error("the successfully-ingested ticket was not reconciled after a partial page failure")
	}
}

// TestIngestRepoSkipsReconcileOnNotModified confirms a 304 response short
// circuits before reconcile ever runs — reconcile issues one GetIssue call
// per linked ticket, and paying that cost on every poll even when GitHub
// reported nothing changed would defeat the point of the ETag check.
func TestIngestRepoSkipsReconcileOnNotModified(t *testing.T) {
	gdb, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	repo := newRepo(t, gdb)
	cursor := time.Now().Add(-time.Hour).Truncate(time.Second)
	if err := gdb.Model(repo).Updates(map[string]any{
		"last_issue_sync": cursor, "etag": "match-me",
	}).Error; err != nil {
		t.Fatalf("seed cursor/etag: %v", err)
	}
	repo.LastIssueSync = &cursor
	repo.ETag = "match-me"

	f := github.NewFake()
	f.ETag = "match-me"
	f.AddIssue(github.Issue{Number: 7, Title: "t", State: "open",
		UpdatedAt: time.Now(), Labels: []string{"golem"}})

	s := ghsync.NewSyncer(gdb, f)
	if err := s.IngestRepo(context.Background(), repo); err != nil {
		t.Fatalf("IngestRepo: %v", err)
	}

	for _, c := range f.CallsSnapshot() {
		if c == "GetIssue" {
			t.Error("reconcile ran on a NotModified poll — GetIssue should never be called")
		}
	}
}
