package ghsync_test

import (
	"context"
	"strings"
	"testing"

	"github.com/leonp92/golem/internal/github"
	"github.com/leonp92/golem/internal/orchestrator/db"
	"github.com/leonp92/golem/internal/orchestrator/ghsync"
	"gorm.io/gorm"
)

// seedPRTicket creates a repo and a ticket that already has a pull request.
func seedPRTicket(t *testing.T, gdb *gorm.DB, phase string, attempts int) *db.Ticket {
	t.Helper()
	newRepo(t, gdb)
	tk := db.Ticket{
		ID: "t1", RepoRemote: "https://github.com/org/repo",
		Title: "Add rate limiting", Branch: "ticket/add-rate-limiting-t1",
		BaseBranch: "main", Description: "d", Phase: phase,
		IssueNumber: intPtr(7), PRNumber: intPtr(42),
		PRURL: "https://github.com/org/repo/pull/42", PRFixAttempts: attempts,
	}
	if err := gdb.Create(&tk).Error; err != nil {
		t.Fatalf("seed ticket: %v", err)
	}
	return &tk
}

func reload(t *testing.T, gdb *gorm.DB) db.Ticket {
	t.Helper()
	var tk db.Ticket
	if err := gdb.First(&tk, "id = ?", "t1").Error; err != nil {
		t.Fatalf("reload ticket: %v", err)
	}
	return tk
}

func boolPtr(b bool) *bool { return &b }

// A green, mergeable pull request must be left entirely alone. This is the
// common case — it runs on every tick for every open pull request — so any
// write here is a write that happens forever.
func TestMonitorPRs_HealthyPullRequestIsUntouched(t *testing.T) {
	gdb, _ := db.Open(":memory:")
	seedPRTicket(t, gdb, "ready-for-review", 0)
	f := github.NewFake()
	ghsync.NewSyncer(gdb, f).MonitorPullRequests(context.Background())

	tk := reload(t, gdb)
	if tk.Phase != "ready-for-review" {
		t.Errorf("phase = %q, want ready-for-review", tk.Phase)
	}
	if tk.PRFixAttempts != 0 {
		t.Errorf("attempts = %d, want 0", tk.PRFixAttempts)
	}
}

// A failing check sends the ticket back to revising with the failure
// described, which is the existing path a human's "request changes" takes.
func TestMonitorPRs_FailingCheckSendsTicketToRevising(t *testing.T) {
	gdb, _ := db.Open(":memory:")
	seedPRTicket(t, gdb, "ready-for-review", 0)
	f := github.NewFake()
	f.PRStatuses = map[int]github.PullRequestStatus{42: {
		Number: 42, State: "open", Mergeable: boolPtr(true),
		MergeableState: "unstable", HeadSHA: "abc123", BaseRef: "main",
	}}
	f.FailedChecks = map[string][]github.CheckFailure{"abc123": {
		{Name: "Test", Conclusion: "failure", Summary: "TestFoo: want 3 got 4",
			DetailsURL: "https://github.com/org/repo/runs/1"},
	}}
	ghsync.NewSyncer(gdb, f).MonitorPullRequests(context.Background())

	tk := reload(t, gdb)
	if tk.Phase != "revising" {
		t.Fatalf("phase = %q, want revising", tk.Phase)
	}
	if tk.PRFixAttempts != 1 {
		t.Errorf("attempts = %d, want 1", tk.PRFixAttempts)
	}
	var entries []db.LogEntry
	gdb.Where("ticket_id = ?", "t1").Find(&entries)
	var feedback string
	for _, e := range entries {
		if e.EntryType == "HUMAN_FEEDBACK" {
			feedback = e.Message
		}
	}
	// The agent gets only what this message carries, so the check's name
	// and its own summary both have to be in it.
	for _, want := range []string{"Test", "TestFoo: want 3 got 4"} {
		if !strings.Contains(feedback, want) {
			t.Errorf("feedback does not mention %q; the agent cannot act on what it is not told:\n%s", want, feedback)
		}
	}
}

// A conflict is reported as a conflict, with the base branch named, because
// resolving it means merging that specific branch.
func TestMonitorPRs_ConflictSendsTicketToRevising(t *testing.T) {
	gdb, _ := db.Open(":memory:")
	seedPRTicket(t, gdb, "ready-for-review", 0)
	f := github.NewFake()
	f.PRStatuses = map[int]github.PullRequestStatus{42: {
		Number: 42, State: "open", Mergeable: boolPtr(false),
		MergeableState: "dirty", HeadSHA: "abc123", BaseRef: "main",
	}}
	ghsync.NewSyncer(gdb, f).MonitorPullRequests(context.Background())

	tk := reload(t, gdb)
	if tk.Phase != "revising" {
		t.Fatalf("phase = %q, want revising", tk.Phase)
	}
	var entries []db.LogEntry
	gdb.Where("ticket_id = ? AND entry_type = ?", "t1", "HUMAN_FEEDBACK").Find(&entries)
	if len(entries) == 0 {
		t.Fatal("no feedback recorded for the conflict")
	}
	msg := entries[0].Message
	for _, want := range []string{"conflict", "origin/main"} {
		if !strings.Contains(strings.ToLower(msg), strings.ToLower(want)) {
			t.Errorf("conflict feedback does not mention %q:\n%s", want, msg)
		}
	}
}

// mergeable is nil while GitHub computes the merge commit. That is "not yet
// known", and acting on it would send tickets to revising to resolve
// conflicts that do not exist — most often right after the push that opened
// the pull request.
func TestMonitorPRs_UnknownMergeabilityIsNotAConflict(t *testing.T) {
	// Both halves are required, and each is pinned separately because each
	// occurs on its own in practice:
	//   - mergeable null + state "unknown": the ordinary "still computing"
	//     window, right after a push.
	//   - mergeable null + state "dirty": GitHub reports a stale state
	//     alongside a not-yet-computed mergeable. Acting on the state alone
	//     here is the bug this guards — it sends the ticket to revising to
	//     resolve a conflict that may not exist, and burns an attempt.
	for _, c := range []struct {
		name  string
		state string
	}{
		{"still computing", "unknown"},
		{"stale dirty state, mergeable not yet computed", "dirty"},
	} {
		t.Run(c.name, func(t *testing.T) {
			gdb, _ := db.Open(":memory:")
			seedPRTicket(t, gdb, "ready-for-review", 0)
			f := github.NewFake()
			f.PRStatuses = map[int]github.PullRequestStatus{42: {
				Number: 42, State: "open", Mergeable: nil,
				MergeableState: c.state, HeadSHA: "abc123", BaseRef: "main",
			}}
			ghsync.NewSyncer(gdb, f).MonitorPullRequests(context.Background())

			tk := reload(t, gdb)
			if tk.Phase != "ready-for-review" {
				t.Errorf("phase = %q; mergeability GitHub has not computed was treated as a conflict", tk.Phase)
			}
			if tk.PRFixAttempts != 0 {
				t.Errorf("attempts = %d; an attempt was spent on an unconfirmed conflict", tk.PRFixAttempts)
			}
		})
	}
}

// The cap is the whole defence against an unfixable failure looping forever.
func TestMonitorPRs_StopsAtTheAttemptCap(t *testing.T) {
	t.Setenv("GOLEM_PR_FIX_ATTEMPTS", "2")
	gdb, _ := db.Open(":memory:")
	seedPRTicket(t, gdb, "ready-for-review", 2)
	f := github.NewFake()
	f.PRStatuses = map[int]github.PullRequestStatus{42: {
		Number: 42, State: "open", Mergeable: boolPtr(true),
		MergeableState: "unstable", HeadSHA: "abc123", BaseRef: "main",
	}}
	f.FailedChecks = map[string][]github.CheckFailure{"abc123": {
		{Name: "Test", Conclusion: "failure"},
	}}
	ghsync.NewSyncer(gdb, f).MonitorPullRequests(context.Background())

	tk := reload(t, gdb)
	if tk.Phase != "needs-attention" {
		t.Errorf("phase = %q, want needs-attention at the cap", tk.Phase)
	}
	if tk.PRFixAttempts != 2 {
		t.Errorf("attempts = %d; the cap must not be exceeded", tk.PRFixAttempts)
	}
}

// Zero means never auto-fix: report and wait. It must not silently behave
// like the default.
func TestMonitorPRs_ZeroCapNeverAttemptsAFix(t *testing.T) {
	t.Setenv("GOLEM_PR_FIX_ATTEMPTS", "0")
	gdb, _ := db.Open(":memory:")
	seedPRTicket(t, gdb, "ready-for-review", 0)
	f := github.NewFake()
	f.PRStatuses = map[int]github.PullRequestStatus{42: {
		Number: 42, State: "open", Mergeable: boolPtr(true),
		MergeableState: "unstable", HeadSHA: "abc123", BaseRef: "main",
	}}
	f.FailedChecks = map[string][]github.CheckFailure{"abc123": {{Name: "Test", Conclusion: "failure"}}}
	ghsync.NewSyncer(gdb, f).MonitorPullRequests(context.Background())

	if tk := reload(t, gdb); tk.Phase != "needs-attention" {
		t.Errorf("phase = %q; a zero cap must report rather than attempt a fix", tk.Phase)
	}
}

// The watch ends when the pull request does. A merged pull request is the
// work landing, so the ticket closes.
func TestMonitorPRs_MergedPullRequestClosesTheTicket(t *testing.T) {
	gdb, _ := db.Open(":memory:")
	seedPRTicket(t, gdb, "ready-for-review", 0)
	f := github.NewFake()
	f.PRStatuses = map[int]github.PullRequestStatus{42: {
		Number: 42, State: "closed", Merged: true, HeadSHA: "abc123", BaseRef: "main",
	}}
	ghsync.NewSyncer(gdb, f).MonitorPullRequests(context.Background())

	if tk := reload(t, gdb); tk.Phase != "closed" {
		t.Errorf("phase = %q, want closed after the pull request merged", tk.Phase)
	}
}

// A pull request closed WITHOUT merging is a human abandoning the work. The
// ticket must not be left in ready-for-review being monitored forever, and
// must not be recorded as if it had landed.
func TestMonitorPRs_ClosedUnmergedPullRequestNeedsAttention(t *testing.T) {
	gdb, _ := db.Open(":memory:")
	seedPRTicket(t, gdb, "ready-for-review", 0)
	f := github.NewFake()
	f.PRStatuses = map[int]github.PullRequestStatus{42: {
		Number: 42, State: "closed", Merged: false, HeadSHA: "abc123", BaseRef: "main",
	}}
	ghsync.NewSyncer(gdb, f).MonitorPullRequests(context.Background())

	if tk := reload(t, gdb); tk.Phase != "needs-attention" {
		t.Errorf("phase = %q; a closed-unmerged pull request needs a human, not silence", tk.Phase)
	}
}

// While the shem is revising, the monitor must not pile on: the checks it
// would read still describe the old head, so it would count a second attempt
// against a fix that is still being written.
func TestMonitorPRs_DoesNotActWhileRevising(t *testing.T) {
	gdb, _ := db.Open(":memory:")
	seedPRTicket(t, gdb, "revising", 1)
	f := github.NewFake()
	f.PRStatuses = map[int]github.PullRequestStatus{42: {
		Number: 42, State: "open", Mergeable: boolPtr(true),
		MergeableState: "unstable", HeadSHA: "abc123", BaseRef: "main",
	}}
	f.FailedChecks = map[string][]github.CheckFailure{"abc123": {{Name: "Test", Conclusion: "failure"}}}
	ghsync.NewSyncer(gdb, f).MonitorPullRequests(context.Background())

	tk := reload(t, gdb)
	if tk.Phase != "revising" {
		t.Errorf("phase = %q, want revising untouched", tk.Phase)
	}
	if tk.PRFixAttempts != 1 {
		t.Errorf("attempts = %d, want 1; the monitor counted an attempt against work in progress", tk.PRFixAttempts)
	}
}

// Some checks cannot be fixed by changing the code, and must not consume
// fix attempts.
//
// Found live: pushing to this project's own pull request produced a CI run
// with conclusion action_required — GitHub holding a fork's workflow for
// maintainer approval. Nothing in the repository causes it and no commit
// clears it; only a person with write access clicking "Approve and run"
// does. Treated as an ordinary failure, the monitor would send the ticket to
// revising, the agent would change something at random, the push would
// produce another run held for approval, and it would repeat until the cap —
// three agent runs and three pushes spent on a permission prompt.
func TestMonitorPRs_ChecksOnlyAHumanCanClearGoStraightToNeedsAttention(t *testing.T) {
	gdb, _ := db.Open(":memory:")
	seedPRTicket(t, gdb, "ready-for-review", 0)
	f := github.NewFake()
	f.PRStatuses = map[int]github.PullRequestStatus{42: {
		Number: 42, State: "open", Mergeable: boolPtr(true),
		MergeableState: "blocked", HeadSHA: "abc123", BaseRef: "main",
	}}
	f.FailedChecks = map[string][]github.CheckFailure{"abc123": {
		{Name: "CI", Conclusion: "action_required", NeedsHuman: true},
	}}
	ghsync.NewSyncer(gdb, f).MonitorPullRequests(context.Background())

	tk := reload(t, gdb)
	if tk.Phase != "needs-attention" {
		t.Errorf("phase = %q, want needs-attention", tk.Phase)
	}
	if tk.PRFixAttempts != 0 {
		t.Errorf("attempts = %d, want 0: no attempt may be spent on a check no commit can clear", tk.PRFixAttempts)
	}
	var entries []db.LogEntry
	gdb.Where("ticket_id = ? AND entry_type = ?", "t1", "STATUS").Find(&entries)
	var found bool
	for _, e := range entries {
		if strings.Contains(e.Message, "CI") && strings.Contains(strings.ToLower(e.Message), "approv") {
			found = true
		}
	}
	if !found {
		t.Error("the status entry does not say which check needs a person or what they must do")
	}
}

// A mix must not let the human-only check hide the fixable one, nor the
// fixable one drag the ticket into a doomed fix loop. A human is required
// either way, so the ticket stops — and the message has to carry both.
func TestMonitorPRs_MixedFailuresStopForTheHumanOne(t *testing.T) {
	gdb, _ := db.Open(":memory:")
	seedPRTicket(t, gdb, "ready-for-review", 0)
	f := github.NewFake()
	f.PRStatuses = map[int]github.PullRequestStatus{42: {
		Number: 42, State: "open", Mergeable: boolPtr(true),
		MergeableState: "blocked", HeadSHA: "abc123", BaseRef: "main",
	}}
	f.FailedChecks = map[string][]github.CheckFailure{"abc123": {
		{Name: "Test", Conclusion: "failure", Summary: "TestFoo failed"},
		{Name: "CI", Conclusion: "action_required", NeedsHuman: true},
	}}
	ghsync.NewSyncer(gdb, f).MonitorPullRequests(context.Background())

	tk := reload(t, gdb)
	if tk.Phase != "needs-attention" {
		t.Errorf("phase = %q, want needs-attention", tk.Phase)
	}
	var entries []db.LogEntry
	gdb.Where("ticket_id = ? AND entry_type = ?", "t1", "STATUS").Find(&entries)
	all := ""
	for _, e := range entries {
		all += e.Message
	}
	for _, want := range []string{"Test", "CI"} {
		if !strings.Contains(all, want) {
			t.Errorf("the human is not told about the %q failure:\n%s", want, all)
		}
	}
}
