package epics

import (
	"context"
	"testing"

	"github.com/leonp92/golem/internal/github"
	"github.com/leonp92/golem/internal/orchestrator/db"
	"gorm.io/gorm"
)

// laneFixture is a running one-node epic whose child ticket has a PR the
// monitor just observed, with the repo row the lane resolves owner/name
// from.
type laneFixture struct {
	gdb    *gorm.DB
	epic   *db.Epic
	ticket *db.Ticket
	gh     *github.Fake
}

func newLaneFixture(t *testing.T, autoMerge bool) *laneFixture {
	t.Helper()
	gdb, _ := db.Open(":memory:")
	e, err := Create(gdb, CreateParams{
		RepoRemote: "https://github.com/org/repo", Title: "x", SpecMarkdown: "s",
		AutoMergeChildren: autoMerge,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	plan := validPlan()
	rev, err := AddRevision(gdb, e.ID, plan, "")
	if err != nil {
		t.Fatalf("revision: %v", err)
	}
	if err := Approve(gdb, e.ID, rev.Revision, rev.PlanHash, "leon"); err != nil {
		t.Fatalf("approve: %v", err)
	}
	if _, err := Start(gdb, e.ID); err != nil {
		t.Fatalf("start: %v", err)
	}
	// Claim node T1's materialized ticket as the PR ticket.
	var tk db.Ticket
	if err := gdb.First(&tk, "epic_id = ? AND epic_node_key = 'T1'", e.ID).Error; err != nil {
		t.Fatalf("child ticket: %v", err)
	}
	n := 42
	gdb.Model(&db.Ticket{}).Where("id = ?", tk.ID).Updates(map[string]any{
		"phase": "ready-for-review", "pr_number": n,
		"pr_url": "https://github.com/org/repo/pull/42",
	})
	tk.Phase, tk.PRNumber = "ready-for-review", &n
	gdb.Create(&db.GitHubRepo{RepoRemote: e.RepoRemote, Owner: "org", Name: "repo"})
	// Create forces TesterRequired on with auto-merge; these tests exercise
	// the lane itself, so they clear the gate the way a satisfied tester
	// verdict will once the verdict store lands.
	gdb.Model(&db.Epic{}).Where("id = ?", e.ID).Update("tester_required", false)
	e.TesterRequired = false
	return &laneFixture{gdb: gdb, epic: e, ticket: &tk, gh: github.NewFake()}
}

func (f *laneFixture) attempts() []db.IntegrationAttempt {
	var out []db.IntegrationAttempt
	f.gdb.Order("id").Find(&out)
	return out
}

func cleanStatus(f *laneFixture) github.PullRequestStatus {
	mergeable := true
	return github.PullRequestStatus{
		Number: 42, State: "open", Mergeable: &mergeable, MergeableState: "clean",
		HeadSHA: "head1", BaseRef: f.epic.IntegrationRef,
	}
}

// Exit: a stale base fails closed — the PR targets something other than
// the plan's integration ref, so nothing merges and the cause is named.
func TestLaneStaleBaseFailsClosed(t *testing.T) {
	f := newLaneFixture(t, true)
	st := cleanStatus(f)
	st.BaseRef = "main"
	if err := AttemptMerge(context.Background(), f.gdb, f.gh, f.ticket, st); err != nil {
		t.Fatalf("lane: %v", err)
	}
	atts := f.attempts()
	if len(atts) != 1 || atts[0].State != "failed-stale" || atts[0].Cause == "" {
		t.Fatalf("attempts = %+v", atts)
	}
	for _, c := range f.gh.CallsSnapshot() {
		if c == "MergePullRequest" {
			t.Fatal("stale base must never reach the merge call")
		}
	}
}

// Exit: a conflict records evidence once per head; the monitor's own
// needsFixing path is what dispatches the fix.
func TestLaneConflictRecordedOncePerHead(t *testing.T) {
	f := newLaneFixture(t, true)
	st := cleanStatus(f)
	dirty := false
	st.Mergeable, st.MergeableState = &dirty, "dirty"
	for i := 0; i < 3; i++ {
		if err := AttemptMerge(context.Background(), f.gdb, f.gh, f.ticket, st); err != nil {
			t.Fatalf("lane: %v", err)
		}
	}
	if atts := f.attempts(); len(atts) != 1 || atts[0].State != "failed-conflict" {
		t.Fatalf("attempts = %+v", atts)
	}
}

// Exit: the happy path merges with the head pinned and the receipt names
// the merge commit, not the head.
func TestLaneMergesAndWritesReceipt(t *testing.T) {
	f := newLaneFixture(t, true)
	st := cleanStatus(f)
	f.gh.PRStatuses = map[int]github.PullRequestStatus{42: {
		Number: 42, State: "open", HeadSHA: "head1", MergeCommitSHA: "merge999",
	}}
	if err := AttemptMerge(context.Background(), f.gdb, f.gh, f.ticket, st); err != nil {
		t.Fatalf("lane: %v", err)
	}
	var node db.EpicNode
	f.gdb.First(&node, "epic_id = ? AND node_key = 'T1'", f.epic.ID)
	if node.IntegratedSHA != "merge999" || node.IntegratedRef != f.epic.IntegrationRef {
		t.Fatalf("receipt = %q into %q", node.IntegratedSHA, node.IntegratedRef)
	}
	if atts := f.attempts(); len(atts) != 1 || atts[0].State != "merged" || atts[0].MergeSHA != "merge999" {
		t.Fatalf("attempts = %+v", atts)
	}
}

// Exit: a crash after the merge call but before the row resolved never
// merges twice — the next pass reconciles the "merging" row from the
// remote and writes the receipt without a second merge call.
func TestLaneCrashAfterMergeDoesNotMergeTwice(t *testing.T) {
	f := newLaneFixture(t, true)
	f.gdb.Create(&db.IntegrationAttempt{
		EpicID: f.epic.ID, EpicNodeKey: "T1", TicketID: f.ticket.ID,
		PRNumber: 42, HeadSHA: "head1", State: "merging",
	})
	f.gh.PRStatuses = map[int]github.PullRequestStatus{42: {
		Number: 42, State: "closed", Merged: true, MergeCommitSHA: "merge999",
		HeadSHA: "head1", BaseRef: f.epic.IntegrationRef,
	}}
	if err := AttemptMerge(context.Background(), f.gdb, f.gh, f.ticket, cleanStatus(f)); err != nil {
		t.Fatalf("lane: %v", err)
	}
	for _, c := range f.gh.CallsSnapshot() {
		if c == "MergePullRequest" {
			t.Fatal("reconcile must not re-merge")
		}
	}
	var node db.EpicNode
	f.gdb.First(&node, "epic_id = ? AND node_key = 'T1'", f.epic.ID)
	if node.IntegratedSHA != "merge999" {
		t.Fatalf("receipt not recovered: %+v", node)
	}
	if atts := f.attempts(); len(atts) != 1 || atts[0].State != "merged" {
		t.Fatalf("attempts = %+v", atts)
	}
}

// Exit: branch protection is honored — GitHub's refusal is permanent for
// the attempt, named on the row, and surfaced to the caller.
func TestLaneProtectedBranchFailsClosed(t *testing.T) {
	f := newLaneFixture(t, true)
	f.gh.PRStatuses = map[int]github.PullRequestStatus{42: {
		Number: 42, State: "closed", HeadSHA: "head1", // fake maps non-open to ErrMergeForbidden
	}}
	err := AttemptMerge(context.Background(), f.gdb, f.gh, f.ticket, cleanStatus(f))
	if err == nil {
		t.Fatal("protection refusal must surface")
	}
	if atts := f.attempts(); len(atts) != 1 || atts[0].State != "failed-forbidden" || atts[0].Cause == "" {
		t.Fatalf("attempts = %+v", atts)
	}
}

// The tester gate fails closed: with TesterRequired on, the head earns a
// blocked row and nothing merges.
func TestLaneTesterRequiredBlocks(t *testing.T) {
	f := newLaneFixture(t, true)
	f.gdb.Model(&db.Epic{}).Where("id = ?", f.epic.ID).Update("tester_required", true)
	if err := AttemptMerge(context.Background(), f.gdb, f.gh, f.ticket, cleanStatus(f)); err != nil {
		t.Fatalf("lane: %v", err)
	}
	if atts := f.attempts(); len(atts) != 1 || atts[0].State != "blocked-tester" {
		t.Fatalf("attempts = %+v", atts)
	}
	for _, c := range f.gh.CallsSnapshot() {
		if c == "MergePullRequest" {
			t.Fatal("tester gate must never reach the merge call")
		}
	}
}

// Auto-merge off means the human merges: the lane is a no-op.
func TestLaneNoOpWithoutAutoMerge(t *testing.T) {
	f := newLaneFixture(t, false)
	if err := AttemptMerge(context.Background(), f.gdb, f.gh, f.ticket, cleanStatus(f)); err != nil {
		t.Fatalf("lane: %v", err)
	}
	if atts := f.attempts(); len(atts) != 0 {
		t.Fatalf("attempts = %+v", atts)
	}
}

// A head that moved during an in-flight attempt reconciles to failed-stale
// and the fresh head earns its own attempt.
func TestLaneHeadMovedDuringAttempt(t *testing.T) {
	f := newLaneFixture(t, true)
	f.gdb.Create(&db.IntegrationAttempt{
		EpicID: f.epic.ID, EpicNodeKey: "T1", TicketID: f.ticket.ID,
		PRNumber: 42, HeadSHA: "oldhead", State: "merging",
	})
	f.gh.PRStatuses = map[int]github.PullRequestStatus{42: {
		Number: 42, State: "open", HeadSHA: "head1", MergeCommitSHA: "merge999",
	}}
	if err := AttemptMerge(context.Background(), f.gdb, f.gh, f.ticket, cleanStatus(f)); err != nil {
		t.Fatalf("lane: %v", err)
	}
	atts := f.attempts()
	if len(atts) != 2 || atts[0].State != "failed-stale" || atts[1].State != "merged" {
		t.Fatalf("attempts = %+v", atts)
	}
}

// Exit: a stale head fails closed — the head moved between the monitor's
// observation and the merge call, GitHub refuses the pin, and the refusal
// is recorded, surfaced, and never silently merged.
func TestLaneStaleHeadFailsClosed(t *testing.T) {
	f := newLaneFixture(t, true)
	f.gh.PRStatuses = map[int]github.PullRequestStatus{42: {
		Number: 42, State: "open", HeadSHA: "someone-elses-push",
	}}
	err := AttemptMerge(context.Background(), f.gdb, f.gh, f.ticket, cleanStatus(f))
	if err == nil {
		t.Fatal("stale head must surface")
	}
	atts := f.attempts()
	if len(atts) != 1 || atts[0].State != "failed-stale" || atts[0].Cause == "" {
		t.Fatalf("attempts = %+v", atts)
	}
	var node db.EpicNode
	f.gdb.First(&node, "epic_id = ? AND node_key = 'T1'", f.epic.ID)
	if node.IntegratedSHA != "" {
		t.Fatal("a stale head must never produce a receipt")
	}
}

// Exit: only a current pass authorizes the merge — fail, inconclusive,
// and stale-SHA verdicts all block, and a pass for the exact head merges.
func TestLaneTesterVerdicts(t *testing.T) {
	newFixture := func(t *testing.T) *laneFixture {
		f := newLaneFixture(t, true)
		f.gdb.Model(&db.Epic{}).Where("id = ?", f.epic.ID).Update("tester_required", true)
		f.gh.PRStatuses = map[int]github.PullRequestStatus{42: {
			Number: 42, State: "open", HeadSHA: "head1", MergeCommitSHA: "merge999",
		}}
		return f
	}
	verdict := func(f *laneFixture, head, v string) {
		if err := RecordVerdict(f.gdb, db.TesterVerdict{
			EpicID: f.epic.ID, EpicNodeKey: "T1", HeadSHA: head, Verdict: v,
			Scenarios: "login flow", Evidence: "see logs", Backend: "local",
		}); err != nil {
			t.Fatalf("record verdict: %v", err)
		}
	}
	lane := func(f *laneFixture) string {
		if err := AttemptMerge(context.Background(), f.gdb, f.gh, f.ticket, cleanStatus(f)); err != nil {
			t.Fatalf("lane: %v", err)
		}
		atts := f.attempts()
		if len(atts) == 0 {
			return ""
		}
		return atts[len(atts)-1].State
	}

	t.Run("fail blocks", func(t *testing.T) {
		f := newFixture(t)
		verdict(f, "head1", "fail")
		if got := lane(f); got != "blocked-tester" {
			t.Fatalf("state = %q", got)
		}
		for _, c := range f.gh.CallsSnapshot() {
			if c == "MergePullRequest" {
				t.Fatal("a fail verdict must never merge")
			}
		}
	})
	t.Run("inconclusive blocks", func(t *testing.T) {
		f := newFixture(t)
		verdict(f, "head1", "inconclusive")
		if got := lane(f); got != "blocked-tester" {
			t.Fatalf("state = %q", got)
		}
	})
	t.Run("stale verdict ignored", func(t *testing.T) {
		f := newFixture(t)
		verdict(f, "an-older-head", "pass")
		if got := lane(f); got != "blocked-tester" {
			t.Fatalf("state = %q — a verdict for another head must not merge", got)
		}
	})
	t.Run("current pass merges", func(t *testing.T) {
		f := newFixture(t)
		// Block first, then deliver the verdict: the block must lift.
		if got := lane(f); got != "blocked-tester" {
			t.Fatalf("pre-verdict state = %q", got)
		}
		verdict(f, "head1", "pass")
		if got := lane(f); got != "merged" {
			t.Fatalf("post-verdict state = %q", got)
		}
		var node db.EpicNode
		f.gdb.First(&node, "epic_id = ? AND node_key = 'T1'", f.epic.ID)
		if node.IntegratedSHA != "merge999" {
			t.Fatalf("receipt = %q", node.IntegratedSHA)
		}
	})
}

// Exit: the lane merges only what GitHub itself calls clean. Pending
// checks, branch protection and an uncomputed mergeability are "not yet",
// not a terminal refusal: nothing is called or recorded, and the next
// tick merges once the state is clean.
func TestLaneWaitsForCleanMergeState(t *testing.T) {
	f := newLaneFixture(t, true)
	f.gh.PRStatuses = map[int]github.PullRequestStatus{42: {Number: 42, State: "open", HeadSHA: "head1", MergeCommitSHA: "m1"}}
	for _, st := range []string{"blocked", "unstable", "unknown", "draft"} {
		s := cleanStatus(f)
		s.MergeableState = st
		if err := AttemptMerge(context.Background(), f.gdb, f.gh, f.ticket, s); err != nil {
			t.Fatalf("%s: %v", st, err)
		}
	}
	s := cleanStatus(f)
	s.Mergeable = nil
	if err := AttemptMerge(context.Background(), f.gdb, f.gh, f.ticket, s); err != nil {
		t.Fatal(err)
	}
	if n := len(f.attempts()); n != 0 {
		t.Fatalf("attempts before clean = %d, want 0", n)
	}
	for _, c := range f.gh.CallsSnapshot() {
		if c == "MergePullRequest" {
			t.Fatal("merged before the PR was clean")
		}
	}
	if err := AttemptMerge(context.Background(), f.gdb, f.gh, f.ticket, cleanStatus(f)); err != nil {
		t.Fatal(err)
	}
	if a := f.attempts(); len(a) != 1 || a[0].State != "merged" {
		t.Fatalf("after clean: %+v", a)
	}
}
