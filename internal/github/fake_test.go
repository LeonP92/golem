package github_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/leonp92/golem/internal/github"
)

// The fake must satisfy the same interface the real client does.
var _ github.Client = github.NewFake()

func TestFakeBehaviour(t *testing.T) {
	ctx := context.Background()
	f := github.NewFake()
	f.AddIssue(github.Issue{Number: 7, Title: "t", State: "open",
		Labels: []string{"golem"}, UpdatedAt: time.Now()})

	page, err := f.ListIssuesSince(ctx, "o", "r", "golem", time.Time{}, "")
	if err != nil {
		t.Fatalf("ListIssuesSince: %v", err)
	}
	if len(page.Issues) != 1 {
		t.Fatalf("got %d issues, want 1", len(page.Issues))
	}

	if _, err := f.ListIssuesSince(ctx, "o", "r", "absent", time.Time{}, ""); err != nil {
		t.Fatalf("ListIssuesSince: %v", err)
	}

	if err := f.AddLabel(ctx, "o", "r", 7, "golem:plan"); err != nil {
		t.Fatalf("AddLabel: %v", err)
	}
	if i, _ := f.GetIssue(ctx, "o", "r", 7); !i.HasLabel("golem:plan") {
		t.Error("AddLabel did not stick")
	}
	if err := f.RemoveLabel(ctx, "o", "r", 7, "golem:plan"); err != nil {
		t.Fatalf("RemoveLabel: %v", err)
	}
	if i, _ := f.GetIssue(ctx, "o", "r", 7); i.HasLabel("golem:plan") {
		t.Error("RemoveLabel did not stick")
	}

	boom := errors.New("boom")
	f.FailNext = boom
	if err := f.CreateComment(ctx, "o", "r", 7, "x"); !errors.Is(err, boom) {
		t.Errorf("FailNext: got %v, want boom", err)
	}
	if err := f.CreateComment(ctx, "o", "r", 7, "x"); err != nil {
		t.Errorf("FailNext should be consumed, got %v", err)
	}
}

// TestFakeCreateAndState exercises CreateIssue, SetIssueState,
// CreatePullRequest, and DefaultBranch, plus the not-found error paths that
// TestFakeBehaviour does not reach.
func TestFakeCreateAndState(t *testing.T) {
	ctx := context.Background()
	f := github.NewFake()

	issue, err := f.CreateIssue(ctx, "o", "r", "new title", "new body", []string{"golem"})
	if err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}
	if issue.Number != 1 || issue.Title != "new title" || issue.State != "open" {
		t.Errorf("issue = %+v, want number 1 / new title / open", issue)
	}

	if err := f.SetIssueState(ctx, "o", "r", issue.Number, "closed"); err != nil {
		t.Fatalf("SetIssueState: %v", err)
	}
	got, err := f.GetIssue(ctx, "o", "r", issue.Number)
	if err != nil {
		t.Fatalf("GetIssue: %v", err)
	}
	if got.State != "closed" {
		t.Errorf("State = %q, want closed", got.State)
	}

	if err := f.SetIssueState(ctx, "o", "r", 999, "closed"); err == nil {
		t.Error("SetIssueState on missing issue: want error, got nil")
	}
	if _, err := f.GetIssue(ctx, "o", "r", 999); err == nil {
		t.Error("GetIssue on missing issue: want error, got nil")
	}

	// RemoveLabel on a missing issue is a no-op, not an error.
	if err := f.RemoveLabel(ctx, "o", "r", 999, "golem"); err != nil {
		t.Errorf("RemoveLabel on missing issue: %v", err)
	}
	// AddLabel on a missing issue is an error.
	if err := f.AddLabel(ctx, "o", "r", 999, "golem"); err == nil {
		t.Error("AddLabel on missing issue: want error, got nil")
	}

	pr1, err := f.CreatePullRequest(ctx, "o", "r", "head", "base", "t", "b", false)
	if err != nil {
		t.Fatalf("CreatePullRequest: %v", err)
	}
	pr2, err := f.CreatePullRequest(ctx, "o", "r", "head2", "base", "t2", "b2", true)
	if err != nil {
		t.Fatalf("CreatePullRequest: %v", err)
	}
	if pr1.Number == pr2.Number {
		t.Errorf("expected distinct PR numbers, got %d and %d", pr1.Number, pr2.Number)
	}

	branch, err := f.DefaultBranch(ctx, "o", "r")
	if err != nil {
		t.Fatalf("DefaultBranch: %v", err)
	}
	if branch != "main" {
		t.Errorf("DefaultBranch = %q, want main", branch)
	}
	f.Default = "develop"
	if branch, err = f.DefaultBranch(ctx, "o", "r"); err != nil || branch != "develop" {
		t.Errorf("DefaultBranch after override = (%q, %v), want develop, nil", branch, err)
	}
}
