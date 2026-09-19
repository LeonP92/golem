package api

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/leonp92/golem/internal/orchestrator/db"
	"github.com/leonp92/golem/internal/orchestrator/ghsync"
	ws "github.com/leonp92/golem/internal/orchestrator/ws"
	"gorm.io/gorm"
)

// This file holds the intake approval gate (spec Amendment 1): the single
// action that releases an externally-sourced ticket for execution, and the
// two refusals it can produce. It is split out of human.go because it is the
// one action on this endpoint that is a security boundary rather than
// workflow bookkeeping — everything a reader needs in order to reason about
// what "approved" means is in this file, and the reasoning is longer than the
// code.

// actionStart releases an externally-ingested ticket for execution, moving it
// to unassigned so a shem can claim it. This is the human checkpoint required
// before any agent prompt is built from an issue body written by a stranger
// (spec Amendment 1).
//
// actionStart is the ONLY writer of intake_approved and approved_body_hash.
// Claimability is enforced against those columns, not against phase (see
// ClaimTicket and availableTickets in tickets.go): phase is a transient
// UX/GitHub-label concern that other handlers legitimately move a ticket
// through (close, needs-attention, requeue, ...), so guarding phase
// transitions one at a time cannot close this off for good — a ticket that
// legitimately leaves pending-approval by any path still carries whatever
// intake_approved was before that transition.
//
// The guard below is deliberately provenance-based (issue_number set,
// intake_approved false, not closed, not claimed) rather than phase-based
// (fix round 4). A GitHub-sourced ticket that leaves pending-approval by any
// route other than start (needs-attention, requeue, ...) still has
// intake_approved=false and is therefore still unclaimable, but under a
// phase-only guard it had NO in-product recovery: one misclick of the
// dashboard's own Close or Re-queue button bricked it permanently, since
// re-ingesting the same issue is blocked by the unique
// (repo_remote, issue_number) index. Matching the guard to the actual
// invariant it establishes — rather than to one specific phase that
// invariant usually starts from — makes that recoverable. A closed ticket
// is deliberately excluded: reopening one is a separate action from
// starting it. assigned_shem IS NULL is also required (fix round 5): the
// phase check alone let a claimed-but-unapproved ticket (a state that
// should not arise, but round 4's guard did not defend against it) get
// double-claimed — see the comment on that clause below.
func (h *Handlers) actionStart(w http.ResponseWriter, r *http.Request, id, reviewedBodyHash string) {
	var ticket db.Ticket
	if err := h.DB.First(&ticket, "id = ?", id).Error; err != nil {
		http.Error(w, "ticket not found", http.StatusNotFound)
		return
	}

	// The release transition and its GitHub label write commit together, so a
	// release can never be recorded without its follow-up queued.
	txErr := h.DB.Transaction(func(tx *gorm.DB) error {
		// assigned_shem IS NULL closes a fix-round-4 double-claim (round 5):
		// the provenance-based guard below checks intake_approved, not
		// claim status, so without this a ticket that is claimed and
		// mid-execution but somehow unapproved was startable — 204, phase
		// moved to unassigned with assigned_shem still set, and a second
		// shem could then claim the same ticket and branch. A claimed
		// ticket has nothing for "start" to do: it was already released
		// once (that is how it got claimed), and un-claiming it is
		// requeue's job, not start's — requeue also pushes ticket_requeued
		// to the running shem, which start still does not do, so start
		// must not perform requeue's job under a different name.
		result := tx.Model(&db.Ticket{}).
			Where("id = ? AND issue_number IS NOT NULL AND intake_approved = false "+
				"AND phase != 'closed' AND assigned_shem IS NULL", id).
			Updates(map[string]any{
				"phase":           "unassigned",
				"intake_approved": true,
			})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return errNotStartable
		}
		// Reload inside the transaction, after its own write, and hash THAT
		// Description (fix round 1b, finding I5). The ticket read above was
		// taken with a plain, pre-transaction h.DB.First, which takes no
		// lock: a ghsync.applyIssue commit landing in the gap meant the
		// approval was stamped with approved_body_hash = H(old) while
		// body_hash had already moved to H(new). The claim predicates
		// require the two to be equal, so that failed closed — but with no
		// in-product recovery: the ticket showed as released, a second
		// start 409ed on intake_approved, and requeue excludes unassigned.
		// Once this transaction has written the row no other writer can
		// commit against it until we commit, so this read sees every
		// committed write plus our own and never anything staler. Same
		// shape as updatePhase (commit 5aaaa5a) and branchPushed.
		//
		// This deliberately does NOT write body_hash, and must never be
		// "tidied up" to do so. body_hash is ghsync's alone — it is the
		// record of what the live issue currently says. Stamping both
		// columns from one read here would make the ticket claimable by
		// construction no matter what the issue had been edited to, which
		// is precisely the gate this branch exists to build. Writing only
		// approved_body_hash keeps the invariant one-directional: approval
		// can only ever certify text that ingest has already hashed.
		var fresh db.Ticket
		if err := tx.First(&fresh, "id = ?", id).Error; err != nil {
			return fmt.Errorf("reload ticket %s: %w", id, err)
		}
		// approvedHash binds this approval to the exact description text
		// that is on the ticket now (spec Amendment 1 fix round 4):
		// ghsync.applyIssue re-gates (clears intake_approved and this hash,
		// and returns the ticket to pending-approval) if a later poll finds
		// the live issue no longer hashes to what was approved here, for any
		// ticket that has not yet been claimed.
		//
		// "Description" is the COMPOSED issue text — title, blank line, body
		// (github.Issue.TicketDescription) — and ghsync writes body_hash over
		// exactly those same bytes, which is what makes the comparison below
		// meaningful rather than accidental. It matters that the title is in
		// there: it is interpolated into the agent prompts along with the
		// rest, so a hash that skipped it would let an edited title through
		// unread. See ghsync.HashDescription.
		approvedHash := ghsync.HashDescription(fresh.Description)

		// ...and reviewedBodyHash binds it to the text the operator was
		// actually SHOWN (fix round 1c). Round 1b closed the window between
		// this handler's own read and its transaction, but the window that
		// matters is longer and cannot be seen from inside this request at
		// all: the time between the page being rendered and the operator
		// clicking Approve, which is however long a human spends reading.
		// An ingest pass landing in there leaves this transaction reading
		// the edited text, and hashing it would certify text nobody
		// reviewed — fail-open, on the one property this gate exists to
		// provide. The page therefore renders the hash it displayed the
		// description at, the control submits it, and it has to still hold
		// here.
		//
		// Both comparisons are required. reviewedBodyHash == fresh.BodyHash
		// is the actual check. approvedHash == fresh.BodyHash additionally
		// refuses to approve a row whose description and body_hash disagree —
		// i.e. one where ghsync's invariant BodyHash ==
		// HashDescription(Description) has been broken by some write site.
		// Nothing produces that state today, and an mid-upgrade row that did
		// (empty body_hash, the S5 shape) would otherwise get a silent 204
		// and be stranded unclaimable with no recovery. Failing loudly here
		// means an approval that returns 204 has always left the ticket
		// genuinely claimable.
		//
		// This check is deliberately after the UPDATE above rather than
		// before it: the row is only held against concurrent writers once
		// this transaction has written it, so a comparison made before that
		// could be overtaken by the very ingest commit it is meant to catch.
		// Refusing rolls the whole transaction back, including the phase and
		// intake_approved writes and any outbox row.
		if reviewedBodyHash != fresh.BodyHash || approvedHash != fresh.BodyHash {
			return errStaleReview
		}

		if err := tx.Model(&db.Ticket{}).Where("id = ?", id).
			Update("approved_body_hash", approvedHash).Error; err != nil {
			return fmt.Errorf("record approved body hash for ticket %s: %w", id, err)
		}
		fresh.ApprovedBodyHash = approvedHash
		return enqueueGitHubPhase(tx, fresh, "unassigned", h.BaseURL)
	})
	if errors.Is(txErr, errStaleReview) {
		http.Error(w, "the GitHub issue changed since this page was loaded; "+
			"reload the ticket and read the new description before approving it", http.StatusConflict)
		return
	}
	if errors.Is(txErr, errNotStartable) {
		http.Error(w, "ticket is already approved, already claimed, not linked to a GitHub issue, or closed", http.StatusConflict)
		return
	}
	if txErr != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	if err := h.appendLog(id, "STATUS", "human", "", "Approved and released for execution"); err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	// Broadcast availability to all shems watching this repo, same as requeue.
	h.Hub.Broadcast(ticket.RepoRemote, ws.WSMessage{
		Type:     "ticket_available",
		TicketID: strPtr(id),
		Repo:     ticket.RepoRemote,
	})
	w.WriteHeader(http.StatusNoContent)
}

// errNotStartable signals that a start action was attempted on a ticket that
// is already approved (intake_approved), already claimed (assigned_shem set),
// not linked to a GitHub issue, or closed.
var errNotStartable = errors.New("ticket is not startable")

// errStaleReview signals that the description the operator approved is no
// longer the description on the ticket — an ingest pass committed an edit
// between the page being rendered and the approval arriving. Recovery is a
// reload: nothing is written, so the operator re-reads the new text and
// approves that.
var errStaleReview = errors.New("ticket body changed since it was reviewed")
