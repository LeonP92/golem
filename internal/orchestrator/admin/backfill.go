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
	// DryRun records which of those two the caller asked for.
	DryRun bool
}

// String renders the result as the one line the subcommand prints.
func (r BackfillResult) String() string {
	verb := "repaired"
	if r.DryRun {
		verb = "would repair"
	}
	return fmt.Sprintf("backfill body_hash: scanned %d ticket(s), %s %d", r.Scanned, verb, r.Repaired)
}

// BackfillBodyHash repairs GitHub-linked tickets whose body_hash is empty.
//
// WHO NEEDS IT. Upgrades from before the GitHub integration are unaffected:
// issue_number did not exist, so every migrated row has issue_number NULL and
// short-circuits the claim predicate. The rows this repairs come from a
// deployment made partway THROUGH that work, when issue_number had shipped
// and body_hash had not — AutoMigrate gave those tickets
// body_hash = "" with a DB-level default, and nothing else ever fills it in.
// A backup restored from such a deployment has the same rows.
//
// WHY THEY DO NOT HEAL THEMSELVES. The claim predicates require
// approved_body_hash = body_hash, so an approved ticket with an empty
// body_hash is unclaimable. Ingest rewrites body_hash only for issues that
// come back from a label-filtered, cursor-bounded list query, and reconcile
// deliberately never writes the ticket row at all, so a quiet issue — one
// nobody edits — is never visited again. The ticket sits in the dashboard
// looking released and no shem can ever take it.
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
// intake_approved and approved_body_hash belong to actionStart alone and are
// never touched here — writing either would let this tool approve text no
// human read. For the same reason this must never be wired into actionStart:
// stamping body_hash from an approval path would make the approval certify
// itself, which is exactly the gate spec Amendment 1 exists to build.
//
// Rows that already carry a body_hash are skipped, never recomputed: a
// non-empty value came from ingest and is the truth about the live issue,
// while ticket.Description is only Golem's copy of it.
//
// A NOTE ON THE DESCRIPTION FORMAT. A row written by a mid-upgrade build
// holds a body-only description, because the title was not composed into it
// yet. This repair hashes that stored text as it stands, which is the right
// thing: it matches the approved_body_hash the operator's approval left
// behind, so the ticket becomes claimable again immediately. The next poll
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
	return res, nil
}
