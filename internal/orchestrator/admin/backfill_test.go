package admin_test

import (
	"context"
	"testing"
	"time"

	"github.com/leonp92/golem/internal/github"
	"github.com/leonp92/golem/internal/orchestrator/admin"
	"github.com/leonp92/golem/internal/orchestrator/db"
	"github.com/leonp92/golem/internal/orchestrator/ghsync"
	"gorm.io/gorm"
)

func openTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	gdb, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	return gdb
}

func intPtr(n int) *int { return &n }

// TestBackfillBodyHash covers which rows the backfill touches and, just as
// importantly, which it must leave alone.
//
// The rows it exists for come from a mid-branch deployment: issue_number had
// shipped but body_hash had not, so AutoMigrate left those tickets with
// body_hash = "". The claim predicates require approved_body_hash = body_hash,
// so such a ticket is permanently unclaimable — and, since fix round 1c, an
// approval on it fails loudly instead of silently, which makes it visible but
// no more recoverable. Ingest only rewrites body_hash for an issue that
// actually comes back from a label-filtered, cursor-bounded list query, and
// reconcile never writes the ticket row at all, so a quiet issue never heals
// itself.
func TestBackfillBodyHash(t *testing.T) {
	const desc = "the issue body a human read"
	hash := ghsync.HashDescription(desc)

	tests := []struct {
		name         string
		ticket       db.Ticket
		wantBodyHash string
		wantRepaired bool
	}{
		{
			name: "github-linked row with an empty body_hash is repaired",
			ticket: db.Ticket{
				ID: "t-empty", RepoRemote: "https://github.com/org/repo",
				Title: "t", Branch: "b", Description: desc,
				Phase: "pending-approval", IssueNumber: intPtr(11),
			},
			wantBodyHash: hash,
			wantRepaired: true,
		},
		{
			name: "the S5 shape: approved, hash stamped, body_hash empty",
			ticket: db.Ticket{
				ID: "t-s5", RepoRemote: "https://github.com/org/repo",
				Title: "t", Branch: "b", Description: desc,
				Phase: "unassigned", IssueNumber: intPtr(12),
				IntakeApproved: true, ApprovedBodyHash: hash,
			},
			wantBodyHash: hash,
			wantRepaired: true,
		},
		{
			name: "a row that already has a body_hash is never rewritten",
			ticket: db.Ticket{
				ID: "t-has", RepoRemote: "https://github.com/org/repo",
				Title: "t", Branch: "b", Description: "something else entirely",
				Phase: "unassigned", IssueNumber: intPtr(13),
				BodyHash: hash,
			},
			wantBodyHash: hash,
			wantRepaired: false,
		},
		{
			name: "a ticket with no linked issue is left alone",
			ticket: db.Ticket{
				ID: "t-local", RepoRemote: "https://github.com/org/repo",
				Title: "t", Branch: "b", Description: desc,
				Phase: "unassigned",
			},
			wantBodyHash: "",
			wantRepaired: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			gdb := openTestDB(t)
			if err := gdb.Create(&tc.ticket).Error; err != nil {
				t.Fatalf("seed ticket: %v", err)
			}

			res, err := admin.BackfillBodyHash(gdb, false)
			if err != nil {
				t.Fatalf("BackfillBodyHash: %v", err)
			}
			wantRepaired := 0
			if tc.wantRepaired {
				wantRepaired = 1
			}
			if res.Repaired != wantRepaired {
				t.Errorf("Repaired = %d, want %d", res.Repaired, wantRepaired)
			}

			var got db.Ticket
			if err := gdb.First(&got, "id = ?", tc.ticket.ID).Error; err != nil {
				t.Fatalf("reload: %v", err)
			}
			if got.BodyHash != tc.wantBodyHash {
				t.Errorf("BodyHash = %q, want %q", got.BodyHash, tc.wantBodyHash)
			}
			// The backfill writes body_hash and nothing else. Writing either
			// of these would make unreviewed text claimable, which is the one
			// thing this tool must never be able to do.
			if got.IntakeApproved != tc.ticket.IntakeApproved {
				t.Errorf("IntakeApproved = %v, want %v (backfill must not touch it)",
					got.IntakeApproved, tc.ticket.IntakeApproved)
			}
			if got.ApprovedBodyHash != tc.ticket.ApprovedBodyHash {
				t.Errorf("ApprovedBodyHash = %q, want %q (backfill must not touch it)",
					got.ApprovedBodyHash, tc.ticket.ApprovedBodyHash)
			}
			if got.Phase != tc.ticket.Phase {
				t.Errorf("Phase = %q, want %q (backfill must not touch it)", got.Phase, tc.ticket.Phase)
			}
		})
	}
}

// TestBackfillBodyHashDryRunWritesNothing verifies the rehearsal an operator
// is told to run first reports the same work without doing any of it.
func TestBackfillBodyHashDryRunWritesNothing(t *testing.T) {
	gdb := openTestDB(t)
	if err := gdb.Create(&db.Ticket{
		ID: "t1", RepoRemote: "https://github.com/org/repo", Title: "t", Branch: "b",
		Description: "body", Phase: "pending-approval", IssueNumber: intPtr(11),
	}).Error; err != nil {
		t.Fatalf("seed: %v", err)
	}

	dry, err := admin.BackfillBodyHash(gdb, true)
	if err != nil {
		t.Fatalf("dry run: %v", err)
	}
	if dry.Repaired != 1 {
		t.Fatalf("dry run Repaired = %d, want 1", dry.Repaired)
	}
	var got db.Ticket
	gdb.First(&got, "id = ?", "t1")
	if got.BodyHash != "" {
		t.Fatalf("dry run wrote BodyHash = %q, want it untouched", got.BodyHash)
	}

	wet, err := admin.BackfillBodyHash(gdb, false)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if wet.Repaired != dry.Repaired {
		t.Errorf("apply repaired %d rows, dry run predicted %d", wet.Repaired, dry.Repaired)
	}
}

// TestBackfillBodyHashIsIdempotent: a second run must find nothing to do,
// so an operator who is unsure whether it already ran can just run it again.
func TestBackfillBodyHashIsIdempotent(t *testing.T) {
	gdb := openTestDB(t)
	if err := gdb.Create(&db.Ticket{
		ID: "t1", RepoRemote: "https://github.com/org/repo", Title: "t", Branch: "b",
		Description: "body", Phase: "pending-approval", IssueNumber: intPtr(11),
	}).Error; err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, err := admin.BackfillBodyHash(gdb, false); err != nil {
		t.Fatalf("first run: %v", err)
	}
	second, err := admin.BackfillBodyHash(gdb, false)
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	if second.Repaired != 0 {
		t.Errorf("second run repaired %d rows, want 0", second.Repaired)
	}
}

// TestBackfillBodyHashMakesTheS5TicketClaimableAgain is the property the
// tool exists for, stated as the claim predicates state it.
func TestBackfillBodyHashMakesTheS5TicketClaimableAgain(t *testing.T) {
	gdb := openTestDB(t)
	const desc = "reviewed text"
	if err := gdb.Create(&db.Ticket{
		ID: "t1", RepoRemote: "https://github.com/org/repo", Title: "t", Branch: "b",
		Description: desc, Phase: "unassigned", IssueNumber: intPtr(11),
		IntakeApproved: true, ApprovedBodyHash: ghsync.HashDescription(desc),
	}).Error; err != nil {
		t.Fatalf("seed: %v", err)
	}

	var before int64
	gdb.Model(&db.Ticket{}).
		Where("issue_number IS NULL OR (intake_approved AND approved_body_hash = body_hash)").
		Count(&before)
	if before != 0 {
		t.Fatalf("the stuck ticket already satisfies the claim predicate (%d); nothing to prove", before)
	}

	if _, err := admin.BackfillBodyHash(gdb, false); err != nil {
		t.Fatalf("backfill: %v", err)
	}

	var after int64
	gdb.Model(&db.Ticket{}).
		Where("issue_number IS NULL OR (intake_approved AND approved_body_hash = body_hash)").
		Count(&after)
	if after != 1 {
		t.Fatalf("ticket still fails the claim predicate after the backfill (%d), want 1", after)
	}
}

// TestBackfillWritesExactlyWhatIngestWouldHave is the coupling that keeps the
// tool useful rather than actively harmful.
//
// The backfill and ghsync both write body_hash, and the approval gate compares
// what they wrote against a hash it recomputes from the description. If the
// backfill ever hashed different bytes — the issue body alone, say, while
// ingest hashes the composed title+body — it would write a value the gate
// rejects and would strand exactly the rows it exists to rescue, while
// reporting success.
//
// So: run a real ingest pass, blank the column the way a mid-upgrade
// AutoMigrate did, run the backfill, and require the restored value to be
// byte-identical to what ingest had written.
func TestBackfillWritesExactlyWhatIngestWouldHave(t *testing.T) {
	tests := []struct {
		name  string
		title string
		body  string
	}{
		{name: "title and body", title: "Add rate limiting", body: "on the login endpoint"},
		{name: "title only", title: "Add a --json flag", body: ""},
		{name: "body with blank lines", title: "Flaky test", body: "first\n\nsecond"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			gdb := openTestDB(t)
			repo := db.GitHubRepo{
				RepoRemote: "https://github.com/org/repo",
				Owner:      "org", Name: "repo", Enabled: true, Label: "golem",
			}
			if err := gdb.Create(&repo).Error; err != nil {
				t.Fatalf("seed repo: %v", err)
			}
			f := github.NewFake()
			f.Default = "main"
			f.AddIssue(github.Issue{
				Number: 7, Title: tc.title, Body: tc.body, State: "open",
				UpdatedAt: time.Now(), Labels: []string{"golem"},
			})
			if err := ghsync.NewSyncer(gdb, f).IngestRepo(context.Background(), &repo); err != nil {
				t.Fatalf("IngestRepo: %v", err)
			}

			var ingested db.Ticket
			if err := gdb.First(&ingested).Error; err != nil {
				t.Fatalf("load ticket: %v", err)
			}
			if ingested.BodyHash == "" {
				t.Fatal("ingest wrote no body_hash; this test has nothing to compare against")
			}

			// What a mid-upgrade AutoMigrate leaves behind.
			if err := gdb.Model(&db.Ticket{}).Where("id = ?", ingested.ID).
				Update("body_hash", "").Error; err != nil {
				t.Fatalf("blank body_hash: %v", err)
			}

			res, err := admin.BackfillBodyHash(gdb, false)
			if err != nil {
				t.Fatalf("BackfillBodyHash: %v", err)
			}
			if res.Repaired != 1 {
				t.Fatalf("Repaired = %d, want 1", res.Repaired)
			}

			var repaired db.Ticket
			if err := gdb.First(&repaired, "id = ?", ingested.ID).Error; err != nil {
				t.Fatalf("reload: %v", err)
			}
			if repaired.BodyHash != ingested.BodyHash {
				t.Errorf("backfill wrote %q, ingest had written %q — the gate compares "+
					"against this and would refuse the ticket",
					repaired.BodyHash, ingested.BodyHash)
			}
			if repaired.BodyHash != ghsync.HashDescription(repaired.Description) {
				t.Error("backfilled body_hash does not hash the row's own description")
			}
		})
	}
}
