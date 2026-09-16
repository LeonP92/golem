package ghsync_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"gorm.io/gorm"

	"github.com/leonp92/golem/internal/github"
	"github.com/leonp92/golem/internal/orchestrator/db"
	"github.com/leonp92/golem/internal/orchestrator/ghsync"
)

// TestAppendLogSurfacesItsSequenceQueryFailure covers minor m8. ghsync's
// appendLog discarded the error from its MAX(sequence_num) scan. On a
// failure nextSeq silently fell back to 1 and the Create that followed
// violated idx_ticket_seq for any ticket that already had a log entry — so
// it failed closed, but the operator was shown a unique-constraint violation
// instead of the real cause, and because appendLog's caller is applyIssue
// the whole page was marked a partial failure and the ingest cursor froze on
// that misleading error.
//
// Verified before the fix: repo.LastError was
// "UNIQUE constraint failed: log_entries.ticket_id, log_entries.sequence_num".
func TestAppendLogSurfacesItsSequenceQueryFailure(t *testing.T) {
	gdb, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	repo := newRepo(t, gdb)

	// A ticket that is approved and already has a log entry, so the fallback
	// sequence number of 1 collides.
	tk := db.Ticket{
		ID: "t1", RepoRemote: repo.RepoRemote, Title: "t", Branch: "b",
		BaseBranch: "main", Description: "approved body", Phase: "unassigned",
		IssueNumber: intPtr(7), IntakeApproved: true,
		ApprovedBodyHash: ghsync.HashBody("approved body"),
		BodyHash:         ghsync.HashBody("approved body"),
	}
	if err := gdb.Create(&tk).Error; err != nil {
		t.Fatalf("seed ticket: %v", err)
	}
	if err := gdb.Create(&db.LogEntry{
		TicketID: "t1", SequenceNum: 1, EntryType: "STATUS",
		FromRole: "system", Message: "first", CreatedAt: time.Now(),
	}).Error; err != nil {
		t.Fatalf("seed log entry: %v", err)
	}

	// Fail only the MAX(sequence_num) lookup, leaving the insert that
	// follows it working — the interleaving that made the old code fall
	// back to sequence 1.
	name := "test:fail_max_sequence_query"
	// Scan runs through the Row callback chain, not the Query one.
	if err := gdb.Callback().Row().Before("gorm:row").Register(name, func(tx *gorm.DB) {
		if tx.Statement == nil {
			return
		}
		if _, ok := tx.Statement.Model.(*db.LogEntry); !ok {
			return
		}
		if strings.Contains(strings.ToUpper(strings.Join(tx.Statement.Selects, ",")), "MAX(") {
			tx.AddError(gorm.ErrInvalidDB)
		}
	}); err != nil {
		t.Fatalf("register callback: %v", err)
	}
	t.Cleanup(func() {
		if err := gdb.Callback().Row().Remove(name); err != nil {
			t.Errorf("remove callback: %v", err)
		}
	})

	f := github.NewFake()
	f.AddIssue(github.Issue{Number: 7, Title: "t", Body: "edited body", State: "open",
		Labels: []string{"golem"}, UpdatedAt: time.Now()})
	// The re-gate path is the one that appends a log entry.
	if err := ghsync.NewSyncer(gdb, f).IngestRepo(context.Background(), repo); err != nil {
		t.Fatalf("IngestRepo: %v", err)
	}

	var got db.GitHubRepo
	if err := gdb.First(&got, repo.ID).Error; err != nil {
		t.Fatalf("reload repo: %v", err)
	}
	if !strings.Contains(got.LastError, "next log sequence") {
		t.Errorf("LastError = %q, want the sequence-query failure rather than the "+
			"unique-constraint violation it used to be mistaken for", got.LastError)
	}
}
