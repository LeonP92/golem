package api

// epicWorkerGate excludes pre-epic workers, which ignore the tester claim payload.
const epicWorkerGate = "(epic_id IS NULL OR EXISTS (SELECT 1 FROM shems WHERE shems.id = ? AND shems.supports_epics))"

// Epic claim gates, added to every claim path. Ordinary tickets
// (epic_id IS NULL) pass untouched.
//
// epicGatePool (available, claim): the run is running on the revision the
// child came from AND every hard predecessor is integrated.
// epicGateResume (resumable, revise claim, reclaim): the run is still
// running on that revision; readiness cannot regress.
//
// SQL, not Go, so ClaimTicket's conditional UPDATE stays atomic.
const (
	epicGatePool = "(epic_id IS NULL OR (epic_ready AND epic_id IN " +
		"(SELECT id FROM epics WHERE state = 'running' AND epics.approved_revision = tickets.epic_plan_revision)))"
	epicGateResume = "(epic_id IS NULL OR epic_id IN " +
		"(SELECT id FROM epics WHERE state = 'running' AND epics.approved_revision = tickets.epic_plan_revision))"
)
