package epics

import (
	"context"
	"strings"
	"testing"

	"github.com/leonp92/golem/internal/github"
	"github.com/leonp92/golem/internal/orchestrator/db"
	"gorm.io/gorm"
)

// laneFixture is a running epic whose child T1 has a ready-for-review PR.
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
	rev, err := AddRevision(gdb, e.ID, validPlan(), "")
	if err != nil {
		t.Fatalf("revision: %v", err)
	}
	if err := Approve(gdb, e.ID, rev.Revision, rev.PlanHash, "leon"); err != nil {
		t.Fatalf("approve: %v", err)
	}
	if _, err := Start(gdb, e.ID); err != nil {
		t.Fatalf("start: %v", err)
	}
	var tk db.Ticket
	if err := gdb.First(&tk, "epic_id = ? AND epic_node_key = 'T1'", e.ID).Error; err != nil {
		t.Fatalf("child ticket: %v", err)
	}
	n := 42
	gdb.Model(&tk).Updates(map[string]any{"phase": "ready-for-review", "pr_number": n})
	tk.Phase, tk.PRNumber = "ready-for-review", &n
	return &laneFixture{gdb: gdb, epic: e, ticket: &tk, gh: github.NewFake()}
}

func (f *laneFixture) clean() github.PullRequestStatus {
	ok := true
	return github.PullRequestStatus{Number: 42, State: "open", Mergeable: &ok, MergeableState: "clean",
		HeadSHA: "head1", BaseRef: f.epic.IntegrationRef}
}

func (f *laneFixture) run(t *testing.T, st github.PullRequestStatus) (fix, park string) {
	t.Helper()
	fix, park, err := MergeChild(context.Background(), f.gdb, f.gh, "org", "repo", f.ticket, st)
	if err != nil {
		t.Fatalf("lane: %v", err)
	}
	return fix, park
}

func (f *laneFixture) merged() bool {
	for _, c := range f.gh.CallsSnapshot() {
		if c == "MergePullRequest" {
			return true
		}
	}
	return false
}

func (f *laneFixture) receipt() string {
	var tk db.Ticket
	f.gdb.First(&tk, "id = ?", f.ticket.ID)
	return tk.EpicIntegratedSHA
}

func (f *laneFixture) verdict(head, v string) {
	if err := RecordVerdict(f.gdb, db.TesterVerdict{EpicID: f.epic.ID, EpicNodeKey: "T1",
		HeadSHA: head, Verdict: v, Evidence: "ev"}); err != nil {
		panic(err)
	}
}

// No verdict for this head waits; a stale-head pass is ignored; fail goes
// back for a fix; inconclusive parks; a current pass merges, pinned to the
// head, and the receipt names the merge commit.
func TestLaneTesterGate(t *testing.T) {
	f := newLaneFixture(t, true)
	if !f.epic.TesterRequired {
		t.Fatal("auto-merge must force the tester gate")
	}
	f.gh.PRStatuses = map[int]github.PullRequestStatus{42: {Number: 42, State: "open", HeadSHA: "head1", MergeCommitSHA: "merge999"}}

	f.verdict("old-head", "pass")
	if fix, park := f.run(t, f.clean()); fix != "" || park != "" || f.merged() {
		t.Fatalf("no current verdict: fix=%q park=%q merged=%t", fix, park, f.merged())
	}
	f.verdict("head1", "fail")
	if fix, _ := f.run(t, f.clean()); !strings.Contains(fix, "tester failed") || f.merged() {
		t.Fatalf("fail verdict: fix=%q merged=%t", fix, f.merged())
	}
	f.verdict("head1", "inconclusive")
	if _, park := f.run(t, f.clean()); park == "" || f.merged() {
		t.Fatalf("inconclusive verdict: park=%q merged=%t", park, f.merged())
	}
	f.verdict("head1", "pass")
	if fix, park := f.run(t, f.clean()); fix != "" || park != "" || !f.merged() {
		t.Fatalf("pass verdict: fix=%q park=%q merged=%t", fix, park, f.merged())
	}
	if got := f.receipt(); got != "merge999" {
		t.Fatalf("receipt = %q, want the merge commit", got)
	}
}

func TestLaneNoOps(t *testing.T) {
	// Auto-merge off: the human merges.
	f := newLaneFixture(t, false)
	if fix, park := f.run(t, f.clean()); fix != "" || park != "" || f.merged() {
		t.Fatal("lane acted without auto-merge")
	}
	// Not clean yet: pending checks, protection, uncomputed mergeability.
	f = newLaneFixture(t, true)
	f.verdict("head1", "pass")
	for _, state := range []string{"blocked", "unstable", "unknown", ""} {
		st := f.clean()
		st.MergeableState = state
		if f.run(t, st); f.merged() {
			t.Fatalf("merged in state %q", state)
		}
	}
	st := f.clean()
	st.Mergeable = nil
	if f.run(t, st); f.merged() {
		t.Fatal("merged with uncomputed mergeability")
	}
	// Paused: no merge until resumed.
	if err := Pause(f.gdb, f.epic.ID); err != nil {
		t.Fatal(err)
	}
	if f.run(t, f.clean()); f.merged() {
		t.Fatal("a paused run merged")
	}
	if err := Resume(f.gdb, f.epic.ID); err != nil {
		t.Fatal(err)
	}
	if f.run(t, f.clean()); !f.merged() {
		t.Fatal("no merge after resume")
	}
}

// Refusals fail closed: a PR on the wrong base or a protected branch
// parks the ticket; a head that moved before the merge call waits for the
// next pass.
func TestLaneRefusals(t *testing.T) {
	f := newLaneFixture(t, true)
	f.verdict("head1", "pass")
	st := f.clean()
	st.BaseRef = "main"
	if _, park := f.run(t, st); park == "" || f.merged() {
		t.Fatalf("wrong base: park=%q merged=%t", park, f.merged())
	}

	f = newLaneFixture(t, true)
	f.verdict("head1", "pass")
	f.gh.MergeErr = map[int]error{42: github.ErrMergeForbidden}
	if _, park := f.run(t, f.clean()); park == "" || f.receipt() != "" {
		t.Fatalf("forbidden: park=%q receipt=%q", park, f.receipt())
	}

	f = newLaneFixture(t, true)
	f.verdict("head1", "pass")
	f.gh.PRStatuses = map[int]github.PullRequestStatus{42: {Number: 42, State: "open", HeadSHA: "head2"}}
	if fix, park := f.run(t, f.clean()); fix != "" || park != "" || f.receipt() != "" {
		t.Fatalf("moved head: fix=%q park=%q receipt=%q", fix, park, f.receipt())
	}
}
