package ghsync_test

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/leonp92/golem/internal/orchestrator/db"
	"github.com/leonp92/golem/internal/orchestrator/ghsync"
)

// statement is one SQL statement GORM executed, with whatever the driver
// answered.
type statement struct {
	sql string
	err error
}

// sqlRecorder is a gorm logger that records every statement and its error.
// GORM's callback processor calls Trace for each executed statement, which
// makes this the cheapest way to assert on the SQL a call actually sends and
// on whether the database raised anything.
type sqlRecorder struct {
	logger.Interface
	mu   sync.Mutex
	seen []statement
}

func (r *sqlRecorder) Trace(_ context.Context, _ time.Time, fc func() (string, int64), err error) {
	sql, _ := fc()
	r.mu.Lock()
	defer r.mu.Unlock()
	r.seen = append(r.seen, statement{sql: sql, err: err})
}

// inserts returns the recorded statements that are INSERTs into the outbox.
func (r *sqlRecorder) inserts() []statement {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []statement
	for _, s := range r.seen {
		if strings.Contains(strings.ToUpper(s.sql), "INSERT INTO") &&
			strings.Contains(s.sql, "git_hub_outboxes") {
			out = append(out, s)
		}
	}
	return out
}

// TestEnqueueUsesOnConflictDoNothing is the CI-visible regression test for
// finding C1, which until now had none that CI could run.
//
// C1 is that Enqueue must not let the database RAISE on a duplicate
// idempotency key. Enqueue runs inside the caller's transaction — that is the
// entire point of an outbox — and on PostgreSQL a raised 23505 puts the whole
// transaction into the aborted state: the next statement fails 25P02, and with
// no next statement the commit itself returns "commit unexpectedly resulted in
// rollback". Catching the violation afterwards therefore silently discarded
// the caller's ticket write. ON CONFLICT DO NOTHING makes the server raise
// nothing at all.
//
// The existing regressions for this live in api/postgres_outbox_test.go and
// are t.Skip'd unless GOLEM_TEST_POSTGRES_DSN is set. On SQLite the two
// implementations are behaviourally identical from the caller's point of view
// — both return nil, both leave one row — so reverting the fix passed
// `go test ./...` green. It was the only fix on this branch with nothing
// defending it.
//
// These two assertions distinguish them on any dialect:
//
//   - the generated SQL carries the conflict clause, so the intent cannot be
//     refactored away silently;
//   - the driver answered no error for the duplicate insert, which is the
//     property that actually matters and the one a plain Create cannot have.
func TestEnqueueUsesOnConflictDoNothing(t *testing.T) {
	gdb, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}

	key := ghsync.CommentKey("t1", "spec")
	row := func() db.GitHubOutbox {
		return db.GitHubOutbox{
			TicketID:       "t1",
			Kind:           ghsync.KindComment,
			Payload:        `{"body":"spec ready"}`,
			IdempotencyKey: key,
		}
	}
	if err := ghsync.Enqueue(gdb, row()); err != nil {
		t.Fatalf("seed the first row: %v", err)
	}

	rec := &sqlRecorder{Interface: gdb.Logger}
	watched := gdb.Session(&gorm.Session{Logger: rec})

	// The duplicate goes in inside a transaction that then does more work,
	// exactly as enqueueGitHubPhase does: a raise here would poison
	// everything after it.
	txErr := watched.Transaction(func(tx *gorm.DB) error {
		if err := ghsync.Enqueue(tx, row()); err != nil {
			return err
		}
		// The caller's own write, after the duplicate. On PostgreSQL this is
		// the statement that used to fail 25P02.
		return tx.Create(&db.Ticket{
			ID: "t1", RepoRemote: "https://github.com/org/repo",
			Title: "t", Branch: "b", Description: "d", Phase: "unassigned",
		}).Error
	})
	if txErr != nil {
		t.Fatalf("the caller's transaction failed after a duplicate enqueue: %v", txErr)
	}

	got := rec.inserts()
	if len(got) != 1 {
		t.Fatalf("recorded %d outbox INSERTs, want exactly 1: %v", len(got), got)
	}
	dup := got[0]
	if !strings.Contains(strings.ToUpper(dup.sql), "ON CONFLICT DO NOTHING") {
		t.Errorf("the outbox INSERT carries no ON CONFLICT DO NOTHING clause, so a duplicate "+
			"key makes the database raise — on PostgreSQL that aborts the caller's whole "+
			"transaction (finding C1).\nSQL: %s", dup.sql)
	}
	if dup.err != nil {
		t.Errorf("the database raised %v on the duplicate insert; it must raise nothing, "+
			"because Enqueue runs inside the caller's transaction (finding C1).\nSQL: %s",
			dup.err, dup.sql)
	}

	// And the de-duplication still works: one row, and the caller's write
	// committed.
	var rows int64
	gdb.Model(&db.GitHubOutbox{}).Where("idempotency_key = ?", key).Count(&rows)
	if rows != 1 {
		t.Errorf("outbox rows for %q = %d, want 1", key, rows)
	}
	var tickets int64
	gdb.Model(&db.Ticket{}).Where("id = ?", "t1").Count(&tickets)
	if tickets != 1 {
		t.Error("the caller's ticket write did not survive the duplicate enqueue")
	}
}
