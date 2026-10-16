package epics

import (
	"context"
	"strings"
	"testing"

	"github.com/leonp92/golem/internal/github"
	"github.com/leonp92/golem/internal/orchestrator/db"
	"gorm.io/gorm"
)

func awaitingFinalReview(t *testing.T, target string, autoFinal bool) (*gorm.DB, *db.Epic) {
	t.Helper()
	gdb, _ := db.Open(":memory:")
	e, err := Create(gdb, CreateParams{
		RepoRemote: "https://github.com/org/repo", Title: "x", SpecMarkdown: "s",
		DeliveryTarget: target, AutoMergeFinal: autoFinal,
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
	for _, key := range []string{"T1", "T2"} {
		if err := RecordIntegration(gdb, e.ID, key, "sha-"+key); err != nil {
			t.Fatalf("receipt %s: %v", key, err)
		}
	}
	var fresh db.Epic
	gdb.First(&fresh, "id = ?", e.ID)
	if fresh.State != "awaiting-final-review" {
		t.Fatalf("state = %q, want awaiting-final-review", fresh.State)
	}
	gdb.Create(&db.GitHubRepo{RepoRemote: e.RepoRemote, Owner: "org", Name: "repo"})
	return gdb, &fresh
}

func finalPR(t *testing.T, gh *github.Fake, ref, head string) int {
	t.Helper()
	pr, err := gh.CreatePullRequest(context.Background(), "org", "repo", ref, "main", "t", "b", false)
	if err != nil {
		t.Fatalf("create pr: %v", err)
	}
	gh.PRStatuses = map[int]github.PullRequestStatus{pr.Number: {
		Number: pr.Number, State: "open", HeadSHA: head, BaseRef: "main",
	}}
	return pr.Number
}

// Exit: the feature-branch target never touches base.
func TestMergeFinalFeatureTargetNeverTouchesBase(t *testing.T) {
	gdb, e := awaitingFinalReview(t, TargetFeatureBranch, false)
	gh := github.NewFake()
	finalPR(t, gh, e.IntegrationRef, "head1")
	err := MergeFinal(context.Background(), gdb, gh, e.ID)
	if err == nil || !strings.Contains(err.Error(), "final merge is yours") {
		t.Fatalf("err = %v", err)
	}
	for _, c := range gh.CallsSnapshot() {
		if c == "MergePullRequest" {
			t.Fatal("feature target must never merge")
		}
	}
}

// Exit: base-branch target merges only after a current pass.
func TestMergeFinalRequiresCurrentPass(t *testing.T) {
	// Missing verdict blocks.
	gdb, e := awaitingFinalReview(t, TargetBaseBranch, true)
	gh := github.NewFake()
	finalPR(t, gh, e.IntegrationRef, "head1")
	if err := MergeFinal(context.Background(), gdb, gh, e.ID); err == nil {
		t.Fatal("no verdict must block")
	}
	// A stale verdict (another head) is ignored.
	RecordVerdict(gdb, db.TesterVerdict{EpicID: e.ID, HeadSHA: "old-head", Verdict: "pass"})
	if err := MergeFinal(context.Background(), gdb, gh, e.ID); err == nil {
		t.Fatal("a stale verdict must block")
	}
	// A fail blocks.
	RecordVerdict(gdb, db.TesterVerdict{EpicID: e.ID, HeadSHA: "head1", Verdict: "fail", Evidence: "login broken"})
	if err := MergeFinal(context.Background(), gdb, gh, e.ID); err == nil || !strings.Contains(err.Error(), "fail") {
		t.Fatalf("fail verdict: err = %v", err)
	}
	// A current pass merges and completes the run.
	RecordVerdict(gdb, db.TesterVerdict{EpicID: e.ID, HeadSHA: "head1", Verdict: "pass"})
	if err := MergeFinal(context.Background(), gdb, gh, e.ID); err != nil {
		t.Fatalf("merge: %v", err)
	}
	var fresh db.Epic
	gdb.First(&fresh, "id = ?", e.ID)
	if fresh.State != "completed" {
		t.Fatalf("state = %q, want completed", fresh.State)
	}
}

// Protected-branch refusals on the final merge surface.
func TestMergeFinalProtectedBranch(t *testing.T) {
	gdb, e := awaitingFinalReview(t, TargetBaseBranch, true)
	gh := github.NewFake()
	num := finalPR(t, gh, e.IntegrationRef, "head1")
	gh.MergeErr = map[int]error{num: github.ErrMergeForbidden}
	RecordVerdict(gdb, db.TesterVerdict{EpicID: e.ID, HeadSHA: "head1", Verdict: "pass"})
	if err := MergeFinal(context.Background(), gdb, gh, e.ID); err == nil {
		t.Fatal("protection refusal must surface")
	}
}

// A run whose children are not all integrated cannot final-merge.
func TestMergeFinalRequiresAllIntegrated(t *testing.T) {
	gdb, _ := db.Open(":memory:")
	e, err := Create(gdb, CreateParams{
		RepoRemote: "https://github.com/org/repo", Title: "x", SpecMarkdown: "s",
		DeliveryTarget: TargetBaseBranch, AutoMergeFinal: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	rev, _ := AddRevision(gdb, e.ID, validPlan(), "")
	Approve(gdb, e.ID, rev.Revision, rev.PlanHash, "leon")
	Start(gdb, e.ID)
	if err := MergeFinal(context.Background(), gdb, github.NewFake(), e.ID); err == nil {
		t.Fatal("a running epic must not final-merge")
	}
}

// Trunk + flags integrates straight into base: the last receipt is the
// delivery, so the run completes instead of waiting on a final PR that
// can never exist.
func TestTrunkFlagsCompletesOnLastReceipt(t *testing.T) {
	gdb, _ := db.Open(":memory:")
	e, err := Create(gdb, CreateParams{RepoRemote: "https://github.com/org/repo", Title: "x",
		SpecMarkdown: "s", BranchStrategy: StrategyTrunkFlags, DeliveryTarget: TargetBaseBranch})
	if err != nil {
		t.Fatal(err)
	}
	rev, _ := AddRevision(gdb, e.ID, validPlan(), "")
	if err := Approve(gdb, e.ID, rev.Revision, rev.PlanHash, "leon"); err != nil {
		t.Fatal(err)
	}
	if _, err := Start(gdb, e.ID); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"T1", "T2"} {
		if err := RecordIntegration(gdb, e.ID, k, "sha-"+k); err != nil {
			t.Fatal(err)
		}
	}
	var fresh db.Epic
	gdb.First(&fresh, "id = ?", e.ID)
	if fresh.State != "completed" {
		t.Fatalf("state = %q, want completed", fresh.State)
	}
}
