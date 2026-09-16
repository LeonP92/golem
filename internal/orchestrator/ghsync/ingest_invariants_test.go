package ghsync_test

import (
	"context"
	"testing"
	"time"

	"github.com/leonp92/golem/internal/github"
	"github.com/leonp92/golem/internal/orchestrator/db"
	"github.com/leonp92/golem/internal/orchestrator/ghsync"
)

// TestIngestNeverOverwritesGolemOwnedFields pins the branch's most-repeated
// claim, which had no test at all: "GitHub is the source of truth for Title
// and Description; Phase, Branch, and BaseBranch are Golem-owned and never
// overwritten here". The invariant is stated in the spec, the plan, the
// ghsync package doc and IngestRepo's own docstring, and the mutation sweep
// found that writing base_branch, branch, or checkpoint_sha on every poll
// passed the entire suite (survivors M6, M7 and M9).
//
// It runs every path applyIssue can take, because each one writes a
// different set of columns, and asserts that the Golem-owned ones come out
// byte-identical. Phase is checked on the two paths that must not move it;
// the other two move it deliberately, and assert the phase they move it to.
func TestIngestNeverOverwritesGolemOwnedFields(t *testing.T) {
	const (
		branch     = "ticket/golem-owned-t1"
		baseBranch = "develop"
		cpPhase    = "implement"
		cpSHA      = "0123456789abcdef0123456789abcdef01234567"
	)

	cases := []struct {
		name string
		// seed customises the ticket before the poll.
		seed func(tk *db.Ticket, shemID uint)
		// issue is the state the poll finds.
		issue     github.Issue
		wantPhase string
		// wantAssigned reports whether assigned_shem must survive the poll.
		wantAssigned bool
	}{
		{
			name: "plain sync of an unapproved ticket",
			seed: func(tk *db.Ticket, _ uint) {
				tk.Phase = "pending-approval"
			},
			issue: github.Issue{
				Number: 7, Title: "edited title", Body: "edited body", State: "open",
				Labels: []string{"golem"}, UpdatedAt: time.Now(),
			},
			wantPhase: "pending-approval",
		},
		{
			name: "plain sync of an approved, unedited ticket",
			seed: func(tk *db.Ticket, _ uint) {
				tk.Phase = "unassigned"
				tk.IntakeApproved = true
				// The composed description for this case's issue, so the
				// poll below genuinely finds nothing changed.
				tk.ApprovedBodyHash = ghsync.HashDescription("edited title\n\nedited body")
				tk.BodyHash = ghsync.HashDescription("edited title\n\nedited body")
			},
			issue: github.Issue{
				Number: 7, Title: "edited title", Body: "edited body", State: "open",
				Labels: []string{"golem"}, UpdatedAt: time.Now(),
			},
			wantPhase: "unassigned",
		},
		{
			name: "re-gate after the body changed",
			seed: func(tk *db.Ticket, _ uint) {
				tk.Phase = "unassigned"
				tk.IntakeApproved = true
				tk.ApprovedBodyHash = ghsync.HashDescription("t\n\napproved body")
				tk.BodyHash = ghsync.HashDescription("t\n\napproved body")
			},
			issue: github.Issue{
				Number: 7, Title: "t", Body: "edited body", State: "open",
				Labels: []string{"golem"}, UpdatedAt: time.Now(),
			},
			wantPhase: "pending-approval",
		},
		{
			name: "body changed under a claimed ticket",
			seed: func(tk *db.Ticket, shemID uint) {
				tk.Phase = "implement"
				tk.AssignedShem = &shemID
				tk.IntakeApproved = true
				tk.ApprovedBodyHash = ghsync.HashDescription("t\n\napproved body")
				tk.BodyHash = ghsync.HashDescription("t\n\napproved body")
			},
			issue: github.Issue{
				Number: 7, Title: "t", Body: "edited body", State: "open",
				Labels: []string{"golem"}, UpdatedAt: time.Now(),
			},
			wantPhase:    "implement",
			wantAssigned: true,
		},
		{
			name: "closed issue closes the ticket and touches nothing else",
			seed: func(tk *db.Ticket, _ uint) {
				tk.Phase = "implement"
			},
			issue: github.Issue{
				Number: 7, Title: "t", Body: "b", State: "closed",
				Labels: []string{"golem"}, UpdatedAt: time.Now(),
			},
			wantPhase: "closed",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gdb, err := db.Open(":memory:")
			if err != nil {
				t.Fatalf("Open: %v", err)
			}
			repo := newRepo(t, gdb)
			shem := db.Shem{Name: "s", APIKeyHash: "x", Repos: "[]", Status: "online"}
			if err := gdb.Create(&shem).Error; err != nil {
				t.Fatalf("seed shem: %v", err)
			}

			phase, sha := cpPhase, cpSHA
			tk := db.Ticket{
				ID: "t1", RepoRemote: repo.RepoRemote, Title: "t",
				Branch: branch, BaseBranch: baseBranch, Description: "approved body",
				CheckpointPhase: &phase, CheckpointSHA: &sha,
				IssueNumber: intPtr(7),
			}
			tc.seed(&tk, shem.ID)
			if err := gdb.Create(&tk).Error; err != nil {
				t.Fatalf("seed ticket: %v", err)
			}

			f := github.NewFake()
			f.AddIssue(tc.issue)
			if err := ghsync.NewSyncer(gdb, f).IngestRepo(context.Background(), repo); err != nil {
				t.Fatalf("IngestRepo: %v", err)
			}

			var got db.Ticket
			if err := gdb.First(&got, "id = ?", "t1").Error; err != nil {
				t.Fatalf("reload ticket: %v", err)
			}

			if got.Branch != branch {
				t.Errorf("branch = %q, want %q — ingest must never write it", got.Branch, branch)
			}
			if got.BaseBranch != baseBranch {
				t.Errorf("base_branch = %q, want %q — ingest must never write it", got.BaseBranch, baseBranch)
			}
			if got.CheckpointPhase == nil || *got.CheckpointPhase != cpPhase {
				t.Errorf("checkpoint_phase = %v, want %q — ingest must never write it", got.CheckpointPhase, cpPhase)
			}
			if got.CheckpointSHA == nil || *got.CheckpointSHA != cpSHA {
				t.Errorf("checkpoint_sha = %v, want %q — ingest must never write it", got.CheckpointSHA, cpSHA)
			}
			if tc.wantAssigned {
				if got.AssignedShem == nil || *got.AssignedShem != shem.ID {
					t.Errorf("assigned_shem = %v, want %d — ingest must never release a claimed ticket",
						got.AssignedShem, shem.ID)
				}
			} else if got.AssignedShem != nil {
				t.Errorf("assigned_shem = %d, want nil — ingest must never claim a ticket", *got.AssignedShem)
			}
			if got.Phase != tc.wantPhase {
				t.Errorf("phase = %q, want %q", got.Phase, tc.wantPhase)
			}

			// The fields GitHub genuinely owns must have moved, or the test
			// above would pass on an ingest that did nothing at all. The
			// description is the COMPOSED title+body, which is what the CLI
			// stores too and what an agent is shown.
			if got.Description != tc.issue.TicketDescription() {
				t.Errorf("description = %q, want %q — ingest must sync the composed issue text",
					got.Description, tc.issue.TicketDescription())
			}
			// The gate's invariant, asserted against the row as stored rather
			// than against what the test thinks was stored.
			if got.BodyHash != ghsync.HashDescription(got.Description) {
				t.Errorf("body_hash does not hash this row's own description; " +
					"the approval gate compares the two and would refuse every approval")
			}
		})
	}
}
