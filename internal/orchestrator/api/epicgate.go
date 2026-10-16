package api

// Epic claim gates (design: "claim predicates must independently require
// approved plan membership, readiness and a running epic ... in available,
// claim, resume, reclaim and revision paths, not just in the UI").
//
// epicGatePool gates the unassigned pool (available + claim + reclaim of a
// reaped child): a child is claimable only while its epic is running on
// the approved revision the child was materialized from AND the child's
// readiness flag says every hard predecessor is integrated. The flag is
// maintained on start and every receipt, but the predicate independently
// re-checks the epic's state and revision — a stored flag alone would let
// a paused/cancelled/re-planned run's leftovers stay claimable.
//
// epicGateResume gates the assigned-resume paths (resumable + revise
// claim): work already claimed keeps running only while the epic is still
// running on the same approved revision. Readiness is not re-checked
// there — receipts are append-only, so a node that was ready when claimed
// cannot become un-ready.
//
// Both clauses are SQL (not Go-side filtering) so the conditional UPDATE
// in ClaimTicket stays atomic: the last gate is the same row lock that
// wins the claim.
const (
	epicGatePool = "(epic_id IS NULL OR (epic_ready AND epic_id IN " +
		"(SELECT id FROM epics WHERE state = 'running' AND epics.approved_revision = tickets.epic_plan_revision)))"
	epicGateResume = "(epic_id IS NULL OR epic_id IN " +
		"(SELECT id FROM epics WHERE state = 'running' AND epics.approved_revision = tickets.epic_plan_revision))"
)
