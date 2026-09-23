package db_test

import (
	"testing"
	"time"

	"github.com/leonp92/golem/internal/orchestrator/db"
)

func TestGitHubModelsMigrate(t *testing.T) {
	gdb, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	repo := db.GitHubRepo{
		RepoRemote: "https://github.com/org/repo",
		Owner:      "org",
		Name:       "repo",
		Enabled:    true,
		Label:      "golem",
	}
	if err := gdb.Create(&repo).Error; err != nil {
		t.Fatalf("create GitHubRepo: %v", err)
	}

	row := db.GitHubOutbox{
		TicketID:       "t1",
		Kind:           "comment",
		Payload:        `{"body":"hi"}`,
		IdempotencyKey: "t1:comment:spec",
		NextAttempt:    time.Now(),
	}
	if err := gdb.Create(&row).Error; err != nil {
		t.Fatalf("create GitHubOutbox: %v", err)
	}
}

func TestOneTicketPerIssue(t *testing.T) {
	gdb, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	n := 7
	mk := func(id string) db.Ticket {
		return db.Ticket{
			ID: id, RepoRemote: "https://github.com/org/repo",
			Title: "t", Branch: "ticket/t-" + id, Description: "d",
			Phase: "unassigned", IssueNumber: &n,
		}
	}
	if err := gdb.Create(&[]db.Ticket{mk("a")}[0]).Error; err != nil {
		t.Fatalf("first insert: %v", err)
	}
	second := mk("b")
	if err := gdb.Create(&second).Error; err == nil {
		t.Fatal("second ticket for the same issue was allowed, want unique violation")
	}

	// Tickets with no issue link must remain freely creatable.
	for _, id := range []string{"c", "d"} {
		tk := db.Ticket{ID: id, RepoRemote: "https://github.com/org/repo",
			Title: "t", Branch: "ticket/t-" + id, Description: "d", Phase: "unassigned"}
		if err := gdb.Create(&tk).Error; err != nil {
			t.Fatalf("nil issue_number insert %s: %v", id, err)
		}
	}
}

func TestOutboxIdempotencyKeyUnique(t *testing.T) {
	gdb, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	mk := func() db.GitHubOutbox {
		return db.GitHubOutbox{TicketID: "t1", Kind: "label", Payload: "{}",
			IdempotencyKey: "t1:label:plan", NextAttempt: time.Now()}
	}
	first := mk()
	if err := gdb.Create(&first).Error; err != nil {
		t.Fatalf("first insert: %v", err)
	}
	second := mk()
	if err := gdb.Create(&second).Error; err == nil {
		t.Fatal("duplicate idempotency key was allowed, want unique violation")
	}
}
