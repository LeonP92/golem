package admin

import (
	"fmt"

	"github.com/leonp92/golem/internal/orchestrator/db"
	"github.com/leonp92/golem/internal/orchestrator/ghsync"
	"gorm.io/gorm"
)

// BackfillResult reports what BackfillBodyHash found and what it did.
type BackfillResult struct {
	// Scanned is the number of GitHub-linked tickets with an empty
	// body_hash that were examined.
	Scanned int
	// Repaired is the number whose body_hash was written (or, in a dry
	// run, would have been written).
	Repaired int
	// Regated is the number of GitHub-linked, unclaimed, non-closed
	// tickets carrying intake_approved with an EMPTY approved_body_hash —
	// the re-review F1 shape — that were returned to pending-approval (or,
	// in a dry run, would have been).
	Regated int
	// DryRun records which of those two the caller asked for.
	DryRun bool
}

// String renders the result as the one line the subcommand prints.
func (r BackfillResult) String() string {
	verb := "repaired"
	if r.DryRun {
		verb = "would repair"
	}
	return fmt.Sprintf("backfill body_hash: scanned %d ticket(s), %s %d, %s %d unbound approval(s) for re-review",
		r.Scanned, verb, r.Repaired, map[bool]string{true: "would return", false: "returned"}[r.DryRun], r.Regated)
}

// BackfillBodyHash repairs GitHub-linked tickets left half-migrated by a
// deployment made partway through the GitHub integration work.
//
// WHO NEEDS IT. Upgrades from before the GitHub integration are unaffected:
// issue_number did not exist, so every migrated row has issue_number NULL and
// short-circuits the claim predicate. Three narrower windows do need it, and
// they fail in two different directions. AutoMigrate adds each missing column
// at its zero value, so the shape a row lands in depends on which columns its
// last writer knew about:
//
//	[094e3f8, f9e87e0)  issue_number only.
//	                    -> intake_approved=0, both hashes ''.
//	                    Fail-CLOSED: unclaimable, and un-approvable too,
//	                    because actionStart refuses a row whose description
//	                    does not hash to its body_hash.
//	[f9e87e0, fef123b)  + intake_approved.
//	                    -> an approved ticket lands intake_approved=1 with
//	                    both hashes ''. Before re-review finding F1 the claim
//	                    predicate read that as approval, because the empty
//	                    string equals itself: fail-OPEN, claimable with no
//	                    hash binding at all, and that build's applyIssue had
//	                    no re-gate either, so the stored text may never have
//	                    been read by anyone. The predicate now refuses it;
//	                    regateUnboundApprovals below is how it gets un-stuck.
//	[fef123b, c2fe234)  + approved_body_hash.
//	                    -> intake_approved=1, approved_body_hash=H(text the
//	                    operator approved), body_hash ''. Fail-CLOSED, and
//	                    the one shape the body_hash write below makes
//	                    claimable again immediately.
//
// A backup restored from any such deployment has the same rows.
//
// WHY THEY DO NOT HEAL THEMSELVES. Ingest rewrites body_hash only for issues
// that come back from a label-filtered, cursor-bounded list query, and
// reconcile deliberately never writes the ticket row at all, so a quiet issue
// — one nobody edits — is never visited again. The ticket sits in the
// dashboard looking released and no shem can ever take it.
//
// WHAT IT WRITES, AND WHAT IT MUST NEVER WRITE. body_hash and nothing else.
// body_hash is ghsync's column: it is the record of what the live issue
// currently says, and this repair restores the invariant every other site
// maintains —
//
//	BodyHash == ghsync.HashDescription(Description)
//
// — using that same function over that row's own stored description. It must
// keep using exactly that function over exactly those bytes. Hashing anything
// else (the issue body alone, say, as an earlier revision of the description
// format did) would write a value the gate then compares against and rejects,
// stranding the very rows this exists to rescue.
//
// GRANTING approval is not this tool's business and never will be: nothing
// here sets intake_approved or writes a non-empty approved_body_hash, because
// either would let it release text no human read. The one approval column it
// touches it only ever CLEARS — see regateUnboundApprovals — which moves a
// ticket away from claimability, not toward it. For the same reason this must
// never be wired into actionStart: stamping body_hash from an approval path
// would make the approval certify itself, which is exactly the gate spec
// Amendment 1 exists to build.
//
// Rows that already carry a body_hash are skipped, never recomputed: a
// non-empty value came from ingest and is the truth about the live issue,
// while ticket.Description is only Golem's copy of it.
//
// A NOTE ON THE DESCRIPTION FORMAT. A row written by a mid-upgrade build
// holds a body-only description, because the title was not composed into it
// yet. This repair hashes that stored text as it stands, which is the right
// thing: for the [fef123b, c2fe234) shape it matches the approved_body_hash
// the operator's approval left behind, so the ticket becomes claimable again
// immediately. The next poll
// then re-composes the description with the title and re-hashes it, which
// re-gates the ticket for one re-read like every other linked ticket in the
// deployment. Both steps are correct and the order does not matter.
//
// Safe to run repeatedly, and safe to run while the orchestrator is up: each
// row is updated under the same "still empty" condition it was selected by,
// so an ingest pass that fills one in first wins and this writes nothing over
// it.
func BackfillBodyHash(gdb *gorm.DB, dryRun bool) (BackfillResult, error) {
	var tickets []db.Ticket
	if err := gdb.
		Where("issue_number IS NOT NULL AND body_hash = ?", "").
		Find(&tickets).Error; err != nil {
		return BackfillResult{}, fmt.Errorf("select tickets needing a body_hash: %w", err)
	}

	res := BackfillResult{Scanned: len(tickets), DryRun: dryRun}
	for _, ticket := range tickets {
		if dryRun {
			res.Repaired++
			continue
		}
		result := gdb.Model(&db.Ticket{}).
			Where("id = ? AND body_hash = ?", ticket.ID, "").
			Update("body_hash", ghsync.HashDescription(ticket.Description))
		if result.Error != nil {
			return res, fmt.Errorf("write body_hash for ticket %s: %w", ticket.ID, result.Error)
		}
		// RowsAffected == 0 means ingest filled this row in between the
		// select and the update. That is the right outcome and not an error,
		// but it is not a repair this tool performed either.
		if result.RowsAffected > 0 {
			res.Repaired++
		}
	}

	regated, err := regateUnboundApprovals(gdb, dryRun)
	if err != nil {
		return res, err
	}
	res.Regated = regated
	return res, nil
}

// regateUnboundApprovals returns to pending-approval every GitHub-linked,
// unclaimed, non-closed ticket that carries intake_approved with an empty
// approved_body_hash — the re-review F1 shape, an approval that was recorded
// before there was anything to bind it to.
//
// This is the recovery half of the F1 fix. The claim predicates now require a
// non-empty approved_body_hash, so such a row is correctly refused; but on its
// own that leaves it with no way forward either, because actionStart only
// starts a ticket whose intake_approved is still false and nothing a human can
// press in the dashboard clears that column. The one thing that would —
// ghsync.applyIssue's re-gate branch — runs only when GitHub actually returns
// the issue, and a stored ETag on a repo nobody edits can suppress that
// indefinitely. Without this, a correct predicate would strand real work.
//
// It performs exactly applyIssue's re-gate, with applyIssue's own conditions:
// assigned_shem IS NULL so a shem mid-run is never yanked, and phase <>
// 'closed' because closing wins outright there too. It clears approval; it
// never grants it, so like the rest of this file it cannot release text no
// human has read. The body_hash loop above has already stamped these rows, so
// after this pass the ticket is in precisely the state the dashboard's Approve
// control expects.
func regateUnboundApprovals(gdb *gorm.DB, dryRun bool) (int, error) {
	scope := gdb.Model(&db.Ticket{}).
		Where("issue_number IS NOT NULL AND intake_approved AND approved_body_hash = ? "+
			"AND assigned_shem IS NULL AND phase <> ?", "", "closed")
	if dryRun {
		var n int64
		if err := scope.Count(&n).Error; err != nil {
			return 0, fmt.Errorf("count approvals needing re-review: %w", err)
		}
		return int(n), nil
	}
	result := scope.Updates(map[string]any{
		"intake_approved":    false,
		"approved_body_hash": "",
		"phase":              "pending-approval",
	})
	if result.Error != nil {
		return 0, fmt.Errorf("return unbound approvals for re-review: %w", result.Error)
	}
	return int(result.RowsAffected), nil
}
