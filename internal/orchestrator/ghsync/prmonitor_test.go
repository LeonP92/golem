package ghsync_test

import (
	"context"
	"errors"
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
		// Assigned, because that is the only way a ticket reaches
		// ready-for-review: a shem did the work. An unassigned one is a
		// separate case with its own test.
		AssignedShem: func() *uint { u := uint(1); return &u }(),
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

// countEntries returns the ticket's log entries of a given type.
func statusEntries(t *testing.T, gdb *gorm.DB) []db.LogEntry {
	t.Helper()
	var entries []db.LogEntry
	gdb.Where("ticket_id = ? AND entry_type = ?", "t1", "STATUS").
		Order("sequence_num asc").Find(&entries)
	return entries
}

// The monitor has to be visible when it is working, not only when it finds
// something wrong.
//
// As first written it wrote to the activity log only on a failure, a
// conflict, or the pull request closing. A healthy pull request produced
// nothing at all, so "watching, all fine" and "not running" looked exactly
// the same from the ticket page — which is how you tell a monitor is dead,
// and you could not tell.
func TestMonitorPRs_RecordsThatItIsWatching(t *testing.T) {
	gdb, _ := db.Open(":memory:")
	seedPRTicket(t, gdb, "ready-for-review", 0)
	f := github.NewFake()
	s := ghsync.NewSyncer(gdb, f)

	s.MonitorPullRequests(context.Background())
	entries := statusEntries(t, gdb)
	if len(entries) != 1 {
		t.Fatalf("got %d status entries on first observation, want 1: %v", len(entries), entries)
	}
	msg := entries[0].Message
	for _, want := range []string{"#42", "passing"} {
		if !strings.Contains(strings.ToLower(msg), strings.ToLower(want)) {
			t.Errorf("the entry does not say %q:\n%s", want, msg)
		}
	}

	// ...and must then stay quiet. It runs every couple of minutes for as
	// long as the pull request is open, so anything written per pass is
	// written forever and buries the entries that matter.
	s.MonitorPullRequests(context.Background())
	s.MonitorPullRequests(context.Background())
	if entries := statusEntries(t, gdb); len(entries) != 1 {
		t.Errorf("got %d status entries after three passes, want 1: the monitor logs "+
			"every pass and will bury the activity log", len(entries))
	}
}

// A change in what the monitor sees must show up, or the quiet above would
// be indistinguishable from the monitor having stopped.
func TestMonitorPRs_RecordsTheChangeWhenChecksGoRed(t *testing.T) {
	gdb, _ := db.Open(":memory:")
	seedPRTicket(t, gdb, "ready-for-review", 0)
	f := github.NewFake()
	s := ghsync.NewSyncer(gdb, f)
	s.MonitorPullRequests(context.Background()) // healthy: one entry

	f.PRStatuses = map[int]github.PullRequestStatus{42: {
		Number: 42, State: "open", Mergeable: boolPtr(true),
		MergeableState: "unstable", HeadSHA: "abc123", BaseRef: "main",
	}}
	f.FailedChecks = map[string][]github.CheckFailure{"abc123": {{Name: "Lint", Conclusion: "failure"}}}
	s.MonitorPullRequests(context.Background())

	entries := statusEntries(t, gdb)
	if len(entries) < 2 {
		t.Fatalf("checks went red and nothing was recorded: %v", entries)
	}
	last := entries[len(entries)-1].Message
	if !strings.Contains(last, "Lint") {
		t.Errorf("the entry does not name the failing check:\n%s", last)
	}
}

// checksErrClient fails ListFailedChecks the way a token without the Checks
// permission does, while the pull request itself still reads fine.
type checksErrClient struct {
	*github.Fake
	err error
}

func (c *checksErrClient) ListFailedChecks(context.Context, string, string, string) ([]github.CheckFailure, error) {
	return nil, c.err
}

// A token that cannot read checks must not make the monitor silent, and must
// never be mistaken for a green pull request.
//
// Found live: the deployed fine-grained PAT lacked Checks:Read, so every
// pass returned "403 Resource not accessible by personal access token". The
// error aborted the pass before anything was recorded, so the ticket page
// showed nothing at all and the only evidence was in container logs that a
// rebuild then destroyed. From the operator's side this was indistinguishable
// from the monitor not running — which is exactly the complaint that found it.
//
// The greater danger is the other way the error could have been handled: an
// unreadable check list is an EMPTY failure list, and treating that as "no
// failures" reports a red pull request as passing.
func TestMonitorPRs_UnreadableChecksAreReportedNotMistakenForGreen(t *testing.T) {
	gdb, _ := db.Open(":memory:")
	seedPRTicket(t, gdb, "ready-for-review", 0)
	c := &checksErrClient{Fake: github.NewFake(),
		err: errors.New("403 Resource not accessible by personal access token")}
	s := ghsync.NewSyncer(gdb, c)
	s.MonitorPullRequests(context.Background())

	entries := statusEntries(t, gdb)
	if len(entries) == 0 {
		t.Fatal("checks could not be read and nothing was recorded; the monitor is " +
			"invisible exactly when something is wrong with it")
	}
	msg := entries[len(entries)-1].Message
	if strings.Contains(strings.ToLower(msg), "all checks passing") {
		t.Errorf("unreadable checks were reported as passing:\n%s", msg)
	}
	// The operator has to be able to act on it, so the entry names the
	// permission rather than only echoing the API error.
	for _, want := range []string{"Checks", "403"} {
		if !strings.Contains(msg, want) {
			t.Errorf("the entry does not mention %q, so the operator cannot tell "+
				"what to fix:\n%s", want, msg)
		}
	}

	// And it must not churn: a missing permission persists, so it is
	// recorded once and then stays quiet like any other unchanged state.
	s.MonitorPullRequests(context.Background())
	if again := statusEntries(t, gdb); len(again) != len(entries) {
		t.Errorf("entries grew from %d to %d; a persistent permission error is "+
			"logged on every pass", len(entries), len(again))
	}

	// No fix attempt: no commit adds a token permission.
	if tk := reload(t, gdb); tk.PRFixAttempts != 0 || tk.Phase != "ready-for-review" {
		t.Errorf("phase=%q attempts=%d; an unreadable check list must not dispatch the agent",
			tk.Phase, tk.PRFixAttempts)
	}
}

// The activity feed is a column of short lines a person scans. A raw
// go-github error is not one: the live entry came out at 432 characters,
// most of it the request URL and a commit SHA the reader cannot act on,
// with the part that matters — the status and what to grant — at the end.
func TestCondenseAPIError(t *testing.T) {
	raw := "list check runs for zenithflowinc/omnicore-platform@b0e8e5102dc9c724b52738e5c4e181223bf18e40: " +
		"GET https://api.github.com/repos/zenithflowinc/omnicore-platform/commits/" +
		"b0e8e5102dc9c724b52738e5c4e181223bf18e40/check-runs?per_page=100: " +
		"403 Resource not accessible by personal access token []"

	got := ghsync.CondenseAPIErrorForTest(errors.New(raw))
	want := "403 Resource not accessible by personal access token"
	if got != want {
		t.Errorf("condensed = %q, want %q", got, want)
	}

	// Anything that is not a recognisable API error still has to survive
	// legibly rather than vanish — an unrecognised failure is exactly the
	// one worth reading.
	for _, c := range []struct{ in, want string }{
		{"connection refused", "connection refused"},
		{"context deadline exceeded\nwhile dialing", "context deadline exceeded"},
	} {
		if got := ghsync.CondenseAPIErrorForTest(errors.New(c.in)); got != c.want {
			t.Errorf("condensed %q = %q, want %q", c.in, got, c.want)
		}
	}
}

// And the whole entry has to stay short enough to read at a glance.
func TestUnreadableChecksEntryIsShort(t *testing.T) {
	gdb, _ := db.Open(":memory:")
	seedPRTicket(t, gdb, "ready-for-review", 0)
	c := &checksErrClient{Fake: github.NewFake(), err: errors.New(
		"list check runs for org/repo@b0e8e5102dc9c724b52738e5c4e181223bf18e40: " +
			"GET https://api.github.com/repos/org/repo/commits/b0e8e51/check-runs?per_page=100: " +
			"403 Resource not accessible by personal access token []")}
	ghsync.NewSyncer(gdb, c).MonitorPullRequests(context.Background())

	entries := statusEntries(t, gdb)
	if len(entries) == 0 {
		t.Fatal("nothing recorded")
	}
	msg := entries[len(entries)-1].Message
	if len(msg) > 200 {
		t.Errorf("entry is %d characters; too long to scan in the feed:\n%s", len(msg), msg)
	}
	if strings.Contains(msg, "api.github.com") {
		t.Errorf("the request URL is in the entry and the reader cannot act on it:\n%s", msg)
	}
	for _, want := range []string{"403", "Checks"} {
		if !strings.Contains(msg, want) {
			t.Errorf("the entry lost %q while being shortened:\n%s", want, msg)
		}
	}
}

// partialChecksClient sees some failures and fails on the rest, the way a
// token that can read commit statuses but not check runs does.
type partialChecksClient struct {
	*github.Fake
	failures []github.CheckFailure
	err      error
}

func (c *partialChecksClient) ListFailedChecks(context.Context, string, string, string) ([]github.CheckFailure, error) {
	return c.failures, c.err
}

// A failure that IS visible must still be acted on, even when another source
// could not be read.
//
// The deployed token reaches commit statuses but is refused check runs, so
// treating the error as total would throw away real, readable failures and
// leave the pull request sitting red with nobody told.
func TestMonitorPRs_ActsOnVisibleFailuresDespiteAPartialView(t *testing.T) {
	gdb, _ := db.Open(":memory:")
	seedPRTicket(t, gdb, "ready-for-review", 0)
	c := &partialChecksClient{
		Fake:     github.NewFake(),
		failures: []github.CheckFailure{{Name: "ci/lint", Conclusion: "failure", Summary: "unused import"}},
		err:      errors.New("list checks for org/repo@abc: check runs: 403 Resource not accessible by personal access token"),
	}
	ghsync.NewSyncer(gdb, c).MonitorPullRequests(context.Background())

	tk := reload(t, gdb)
	if tk.Phase != "revising" {
		t.Errorf("phase = %q, want revising: a visible failure was discarded because "+
			"another source could not be read", tk.Phase)
	}
	if tk.PRFixAttempts != 1 {
		t.Errorf("attempts = %d, want 1", tk.PRFixAttempts)
	}
}

// But a partial view with nothing found is NOT green. An unreadable source
// means an unknown number of invisible failures.
func TestMonitorPRs_PartialViewWithNoFailuresIsNotReportedGreen(t *testing.T) {
	gdb, _ := db.Open(":memory:")
	seedPRTicket(t, gdb, "ready-for-review", 0)
	c := &partialChecksClient{
		Fake: github.NewFake(), failures: nil,
		err: errors.New("list checks for org/repo@abc: check runs: 403 Resource not accessible by personal access token"),
	}
	ghsync.NewSyncer(gdb, c).MonitorPullRequests(context.Background())

	entries := statusEntries(t, gdb)
	if len(entries) == 0 {
		t.Fatal("nothing recorded for a partial view")
	}
	msg := entries[len(entries)-1].Message
	if strings.Contains(strings.ToLower(msg), "all checks passing") {
		t.Errorf("a partial view was reported as green:\n%s", msg)
	}
	if tk := reload(t, gdb); tk.Phase != "ready-for-review" || tk.PRFixAttempts != 0 {
		t.Errorf("phase=%q attempts=%d; nothing to fix was found, so nothing should have been dispatched",
			tk.Phase, tk.PRFixAttempts)
	}
}

// Moving a ticket to revising is not enough to make anything happen.
//
// The human "request changes" path does three things: it flips the phase,
// it creates a HumanInput of kind feedback, and it pushes ticket_revise to
// the assigned shem. The monitor did only the first, plus a log entry for
// the activity feed — which is not what the shem reads.
//
// The result, observed live on three real tickets: all three moved to
// revising and then sat there. Nothing woke the shem, and because
// resumableTickets deliberately excludes revising, not even a shem restart
// would have picked them up. Had one been woken anyway, consumeFeedback
// reads the HumanInput row, not the log, so the agent would have been told
// to fix a pull request without being told what was wrong with it.
func TestMonitorPRs_DispatchCreatesFeedbackAndWakesTheShem(t *testing.T) {
	gdb, _ := db.Open(":memory:")
	seedPRTicket(t, gdb, "ready-for-review", 0)
	shemID := uint(1) // seedPRTicket assigns this
	f := github.NewFake()
	f.PRStatuses = map[int]github.PullRequestStatus{42: {
		Number: 42, State: "open", Mergeable: boolPtr(true),
		MergeableState: "unstable", HeadSHA: "abc123", BaseRef: "main",
	}}
	f.FailedChecks = map[string][]github.CheckFailure{"abc123": {
		{Name: "Python services", Conclusion: "failure", Summary: "pytest: 3 failed"},
	}}

	s := ghsync.NewSyncer(gdb, f)
	woken := make(chan uint, 4)
	s.NotifyShem = func(shem uint, _, _, _ string) { woken <- shem }
	s.MonitorPullRequests(context.Background())

	if tk := reload(t, gdb); tk.Phase != "revising" {
		t.Fatalf("phase = %q, want revising", tk.Phase)
	}

	// The feedback the agent actually reads.
	var inputs []db.HumanInput
	gdb.Where("ticket_id = ? AND kind = ? AND resolved_at IS NULL", "t1", "feedback").Find(&inputs)
	if len(inputs) != 1 {
		t.Fatalf("got %d unresolved feedback inputs, want 1: consumeFeedback reads this "+
			"row, not the log, so the agent would be sent to fix an unnamed problem", len(inputs))
	}
	for _, want := range []string{"Python services", "pytest: 3 failed"} {
		if !strings.Contains(inputs[0].Prompt, want) {
			t.Errorf("the feedback row does not carry %q:\n%s", want, inputs[0].Prompt)
		}
	}

	// And the shem has to be told, or the ticket sits in revising forever:
	// the poll loop only asks for AVAILABLE tickets, and resumableTickets
	// excludes revising.
	select {
	case got := <-woken:
		if got != shemID {
			t.Errorf("woke shem %d, want %d", got, shemID)
		}
	default:
		t.Error("no shem was woken; the ticket is in revising with nothing working it")
	}
}

// A ticket with no assigned shem must not be dispatched into a state where
// nothing can pick it up.
func TestMonitorPRs_UnassignedTicketIsNotSentToRevising(t *testing.T) {
	gdb, _ := db.Open(":memory:")
	seedPRTicket(t, gdb, "ready-for-review", 0)
	if err := gdb.Model(&db.Ticket{}).Where("id = ?", "t1").
		Update("assigned_shem", nil).Error; err != nil {
		t.Fatalf("unassign: %v", err)
	}
	f := github.NewFake()
	f.PRStatuses = map[int]github.PullRequestStatus{42: {
		Number: 42, State: "open", Mergeable: boolPtr(true),
		MergeableState: "unstable", HeadSHA: "abc123", BaseRef: "main",
	}}
	f.FailedChecks = map[string][]github.CheckFailure{"abc123": {{Name: "Test", Conclusion: "failure"}}}
	ghsync.NewSyncer(gdb, f).MonitorPullRequests(context.Background())

	tk := reload(t, gdb)
	if tk.Phase == "revising" {
		t.Error("an unassigned ticket was moved to revising; no shem owns it, the poll " +
			"loop does not offer revising tickets, and resumableTickets excludes them")
	}
	if tk.Phase != "needs-attention" {
		t.Errorf("phase = %q, want needs-attention", tk.Phase)
	}
}

// A fix must not be dispatched twice for the same commit.
//
// Seen live. The agent fixed #3189, pushed 4950a8d9, and the ticket
// returned to ready-for-review. On the next pass the monitor read the
// failure list and dispatched again — a second attempt against a failure
// that had already been fixed and whose CI had not finished re-running. At
// the default cap of three, two wasted passes like that park a ticket that
// was converging perfectly well.
//
// The head SHA is the discriminator: a failure seen on a commit already
// dispatched for is the SAME failure, not a new one. Only a failure on a
// commit that has not been acted on is worth another attempt.
func TestMonitorPRs_DoesNotDispatchTwiceForTheSameCommit(t *testing.T) {
	gdb, _ := db.Open(":memory:")
	seedPRTicket(t, gdb, "ready-for-review", 0)
	f := github.NewFake()
	f.PRStatuses = map[int]github.PullRequestStatus{42: {
		Number: 42, State: "open", Mergeable: boolPtr(true),
		MergeableState: "unstable", HeadSHA: "sha-one", BaseRef: "main",
	}}
	f.FailedChecks = map[string][]github.CheckFailure{"sha-one": {{Name: "Test", Conclusion: "failure"}}}
	s := ghsync.NewSyncer(gdb, f)

	s.MonitorPullRequests(context.Background())
	if tk := reload(t, gdb); tk.Phase != "revising" || tk.PRFixAttempts != 1 {
		t.Fatalf("first pass: phase=%q attempts=%d, want revising/1", tk.Phase, tk.PRFixAttempts)
	}

	// The shem finishes and the ticket returns to review, but CI has not
	// re-run yet so the same failure is still what the API reports.
	if err := gdb.Model(&db.Ticket{}).Where("id = ?", "t1").
		Update("phase", "ready-for-review").Error; err != nil {
		t.Fatalf("return to review: %v", err)
	}
	s.MonitorPullRequests(context.Background())

	tk := reload(t, gdb)
	if tk.PRFixAttempts != 1 {
		t.Errorf("attempts = %d, want 1: a second attempt was spent on the same commit's "+
			"failure before the fix had even been tested", tk.PRFixAttempts)
	}
	if tk.Phase == "revising" {
		t.Error("the ticket was dispatched again for a commit already acted on")
	}
}

// But a failure on a NEW commit is a new failure and must be acted on, or
// the guard above would stop the loop after one attempt.
func TestMonitorPRs_DispatchesAgainWhenTheCommitChanges(t *testing.T) {
	gdb, _ := db.Open(":memory:")
	seedPRTicket(t, gdb, "ready-for-review", 0)
	f := github.NewFake()
	f.PRStatuses = map[int]github.PullRequestStatus{42: {
		Number: 42, State: "open", Mergeable: boolPtr(true),
		MergeableState: "unstable", HeadSHA: "sha-one", BaseRef: "main",
	}}
	f.FailedChecks = map[string][]github.CheckFailure{
		"sha-one": {{Name: "Test", Conclusion: "failure"}},
		"sha-two": {{Name: "Test", Conclusion: "failure"}},
	}
	s := ghsync.NewSyncer(gdb, f)
	s.MonitorPullRequests(context.Background())

	// The fix landed as a new commit and CI failed again on it.
	gdb.Model(&db.Ticket{}).Where("id = ?", "t1").Update("phase", "ready-for-review")
	f.PRStatuses[42] = github.PullRequestStatus{
		Number: 42, State: "open", Mergeable: boolPtr(true),
		MergeableState: "unstable", HeadSHA: "sha-two", BaseRef: "main",
	}
	s.MonitorPullRequests(context.Background())

	if tk := reload(t, gdb); tk.PRFixAttempts != 2 || tk.Phase != "revising" {
		t.Errorf("phase=%q attempts=%d, want revising/2: a genuinely new failure was ignored",
			tk.Phase, tk.PRFixAttempts)
	}
}

// A ticket that has exhausted its attempts must park, even when the commit
// has not changed.
//
// Seen live on ticket ed7b8a9b. The agent ran three times against PR #3191,
// each time concluding it "could not find an actionable code defect to fix"
// — the failing check was infrastructure — and each time making no commits,
// so the head never moved. Attempts reached the cap of 3.
//
// The same-commit guard then short-circuited ahead of the cap check, so the
// ticket never reached needs-attention. It sat in ready-for-review, polled
// every two minutes, doing nothing and saying nothing, with three attempts
// spent and no way for anyone to know it had given up. The guard that stops
// wasted work must not also stop the ticket admitting defeat.
func TestMonitorPRs_ExhaustedTicketParksEvenOnAnUnchangedCommit(t *testing.T) {
	t.Setenv("GOLEM_PR_FIX_ATTEMPTS", "3")
	gdb, _ := db.Open(":memory:")
	seedPRTicket(t, gdb, "ready-for-review", 3)
	if err := gdb.Model(&db.Ticket{}).Where("id = ?", "t1").
		Update("pr_dispatched_sha", "abc123").Error; err != nil {
		t.Fatalf("seed dispatched sha: %v", err)
	}
	f := github.NewFake()
	f.PRStatuses = map[int]github.PullRequestStatus{42: {
		Number: 42, State: "open", Mergeable: boolPtr(true),
		MergeableState: "unstable", HeadSHA: "abc123", BaseRef: "main",
	}}
	f.FailedChecks = map[string][]github.CheckFailure{"abc123": {
		{Name: "Build, Push, Sign, Verify Images", Conclusion: "failure"},
	}}
	ghsync.NewSyncer(gdb, f).MonitorPullRequests(context.Background())

	tk := reload(t, gdb)
	if tk.Phase != "needs-attention" {
		t.Errorf("phase = %q, want needs-attention: the ticket used every attempt and "+
			"then sat in ready-for-review with nothing working it and nothing said", tk.Phase)
	}
	if tk.PRFixAttempts != 3 {
		t.Errorf("attempts = %d, want 3 (unchanged)", tk.PRFixAttempts)
	}
	var entries []db.LogEntry
	gdb.Where("ticket_id = ? AND entry_type = ?", "t1", "STATUS").Find(&entries)
	all := ""
	for _, e := range entries {
		all += e.Message + "\n"
	}
	// The message has to say the agent changed nothing, because that is the
	// part a person needs: three runs that produced no commit means the
	// failure is not one the agent can reach.
	for _, want := range []string{"3", "no new commit"} {
		if !strings.Contains(all, want) {
			t.Errorf("the parking message does not mention %q:\n%s", want, all)
		}
	}
}

// -1 keeps trying for as long as the pull request is open.
//
// The cap check and the "no new commit" parking both key off the same
// comparison, so the sentinel has to switch off the cap without switching
// off anything else: a ticket well past the default of 3 must still be
// dispatched, and only for a commit that has not been acted on.
func TestMonitorPRs_UnlimitedAttemptsNeverPark(t *testing.T) {
	t.Setenv("GOLEM_PR_FIX_ATTEMPTS", "-1")
	gdb, _ := db.Open(":memory:")
	seedPRTicket(t, gdb, "ready-for-review", 99) // far past any real cap
	f := github.NewFake()
	f.PRStatuses = map[int]github.PullRequestStatus{42: {
		Number: 42, State: "open", Mergeable: boolPtr(true),
		MergeableState: "unstable", HeadSHA: "fresh-sha", BaseRef: "main",
	}}
	f.FailedChecks = map[string][]github.CheckFailure{"fresh-sha": {{Name: "Test", Conclusion: "failure"}}}
	ghsync.NewSyncer(gdb, f).MonitorPullRequests(context.Background())

	tk := reload(t, gdb)
	if tk.Phase != "revising" {
		t.Errorf("phase = %q, want revising: -1 means keep trying, and 99 attempts "+
			"is not a reason to stop", tk.Phase)
	}
	if tk.PRFixAttempts != 100 {
		t.Errorf("attempts = %d, want 100: the counter still records the work", tk.PRFixAttempts)
	}
}

// Unlimited must not become a tight loop. The same-commit guard is what
// makes -1 safe: an agent that produces no commit cannot re-trigger itself,
// however many attempts remain.
func TestMonitorPRs_UnlimitedStillWillNotRedispatchTheSameCommit(t *testing.T) {
	t.Setenv("GOLEM_PR_FIX_ATTEMPTS", "-1")
	gdb, _ := db.Open(":memory:")
	seedPRTicket(t, gdb, "ready-for-review", 5)
	if err := gdb.Model(&db.Ticket{}).Where("id = ?", "t1").
		Update("pr_dispatched_sha", "same-sha").Error; err != nil {
		t.Fatalf("seed: %v", err)
	}
	f := github.NewFake()
	f.PRStatuses = map[int]github.PullRequestStatus{42: {
		Number: 42, State: "open", Mergeable: boolPtr(true),
		MergeableState: "unstable", HeadSHA: "same-sha", BaseRef: "main",
	}}
	f.FailedChecks = map[string][]github.CheckFailure{"same-sha": {{Name: "Test", Conclusion: "failure"}}}
	ghsync.NewSyncer(gdb, f).MonitorPullRequests(context.Background())

	tk := reload(t, gdb)
	if tk.PRFixAttempts != 5 {
		t.Errorf("attempts = %d, want 5: unlimited re-dispatched a commit already acted "+
			"on, which is a loop every two minutes for as long as the PR is open", tk.PRFixAttempts)
	}
	if tk.Phase != "ready-for-review" {
		t.Errorf("phase = %q, want ready-for-review", tk.Phase)
	}
}

// A human stopped this ticket. The monitor must not overrule them.
//
// Every other automatic path excludes stopped; this one has to as well, or
// a failing check would drag an interrupted ticket straight back into
// revising and the stop would mean nothing.
func TestMonitorPRs_LeavesStoppedTicketsAlone(t *testing.T) {
	gdb, _ := db.Open(":memory:")
	seedPRTicket(t, gdb, "stopped", 0)
	f := github.NewFake()
	f.PRStatuses = map[int]github.PullRequestStatus{42: {
		Number: 42, State: "open", Mergeable: boolPtr(false),
		MergeableState: "dirty", HeadSHA: "abc123", BaseRef: "main",
	}}
	f.FailedChecks = map[string][]github.CheckFailure{"abc123": {{Name: "Test", Conclusion: "failure"}}}
	ghsync.NewSyncer(gdb, f).MonitorPullRequests(context.Background())

	tk := reload(t, gdb)
	if tk.Phase != "stopped" {
		t.Errorf("phase = %q; the monitor restarted a ticket a human had stopped", tk.Phase)
	}
	if tk.PRFixAttempts != 0 {
		t.Errorf("attempts = %d, want 0", tk.PRFixAttempts)
	}
	if len(statusEntries(t, gdb)) != 0 {
		t.Error("the monitor wrote to a stopped ticket; it should not be looking at it at all")
	}

	// Asserted on the CALLS, not only on the outcome. The outcome is
	// defended three deep — the query excludes stopped, monitorPullRequest
	// returns early on any phase but ready-for-review, and the dispatch
	// UPDATE is itself conditional on ready-for-review — so removing any
	// one of them leaves the ticket untouched and a state-only assertion
	// passes while the guard it was written for is gone. Checking that
	// GitHub was never asked about this pull request is the one assertion
	// that fails when the query stops excluding stopped.
	for _, call := range f.CallsSnapshot() {
		if call == "GetPullRequest" || call == "ListFailedChecks" {
			t.Errorf("the monitor called %s for a stopped ticket; a human has "+
				"interrupted it and its pull request should not even be looked at", call)
		}
	}
}

// Closing a ticket because its pull request merged must do everything
// closing it by hand does.
//
// Review finding 3, verified by the reviewer as executed: setPhase wrote
// the phase and a log row and nothing else. api.actionClose additionally
// enqueues KindClose so the GitHub issue is actually closed, clears pending
// HumanInputs, and pushes ticket_closed so the shem removes the worktree
// and the branch. So after every auto-merge the issue stayed open and the
// shem leaked a worktree and a branch — silently, because the ticket looked
// correctly closed from the dashboard.
func TestMonitorPRs_MergeClosesTheIssueAndCleansUp(t *testing.T) {
	gdb, _ := db.Open(":memory:")
	seedPRTicket(t, gdb, "ready-for-review", 0)
	// A pending question that closing must clear, as actionClose does.
	if err := gdb.Create(&db.HumanInput{TicketID: "t1", Kind: "question_answer", Prompt: "?"}).Error; err != nil {
		t.Fatalf("seed input: %v", err)
	}
	f := github.NewFake()
	f.PRStatuses = map[int]github.PullRequestStatus{42: {
		Number: 42, State: "closed", Merged: true, HeadSHA: "abc123", BaseRef: "main",
	}}
	s := ghsync.NewSyncer(gdb, f)
	var notified []string
	s.NotifyShem = func(_ uint, msgType, _, _ string) { notified = append(notified, msgType) }
	s.MonitorPullRequests(context.Background())

	if tk := reload(t, gdb); tk.Phase != "closed" {
		t.Fatalf("phase = %q, want closed", tk.Phase)
	}

	// The GitHub issue has to actually be closed.
	var rows []db.GitHubOutbox
	gdb.Where("ticket_id = ?", "t1").Find(&rows)
	var kinds []string
	for _, r := range rows {
		kinds = append(kinds, r.Kind)
	}
	if !contains(kinds, ghsync.KindClose) {
		t.Errorf("no %s outbox row (%v); the linked issue stays open forever after "+
			"an auto-merge", ghsync.KindClose, kinds)
	}

	// The shem has to be told, or the worktree and branch leak.
	if !contains(notified, "ticket_closed") {
		t.Errorf("the shem was not told the ticket closed (%v); it keeps the worktree "+
			"and the branch", notified)
	}

	// And a question nobody will ever answer must not be left pending.
	var pending int64
	gdb.Model(&db.HumanInput{}).Where("ticket_id = ? AND resolved_at IS NULL", "t1").Count(&pending)
	if pending != 0 {
		t.Errorf("%d unresolved human input(s) left on a closed ticket", pending)
	}
}

func contains(haystack []string, needle string) bool {
	for _, h := range haystack {
		if h == needle {
			return true
		}
	}
	return false
}

// A phase the monitor sets must reach the issue's labels, as one set by
// hand does.
//
// Same finding as the close path, one step smaller: setPhase wrote only
// the phase, so a ticket the monitor parked showed as needs-attention in
// Golem while the GitHub issue still carried the label of whatever phase
// it was in before.
func TestMonitorPRs_NeedsAttentionWritesTheLabel(t *testing.T) {
	gdb, _ := db.Open(":memory:")
	seedPRTicket(t, gdb, "ready-for-review", 0)
	f := github.NewFake()
	f.PRStatuses = map[int]github.PullRequestStatus{42: {
		Number: 42, State: "closed", Merged: false, HeadSHA: "abc123", BaseRef: "main",
	}}
	ghsync.NewSyncer(gdb, f).MonitorPullRequests(context.Background())

	if tk := reload(t, gdb); tk.Phase != "needs-attention" {
		t.Fatalf("phase = %q, want needs-attention", tk.Phase)
	}
	var rows []db.GitHubOutbox
	gdb.Where("ticket_id = ? AND kind = ?", "t1", ghsync.KindLabel).Find(&rows)
	if len(rows) == 0 {
		t.Error("no label row queued; the issue keeps the label of the phase the ticket " +
			"was in before the monitor moved it")
	}
}
