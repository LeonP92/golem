package db

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
	"github.com/leonp92/golem/internal/models"
	"gorm.io/gorm"
)

// User represents an orchestrator web UI user.
type User struct {
	ID           uint   `gorm:"primaryKey"`
	Username     string `gorm:"uniqueIndex;not null"`
	PasswordHash string `gorm:"not null"`
	// Role is an rbac role name ("admin" | "developer"). Least privilege by
	// default; an unrecognised value simply has no permissions.
	Role string `gorm:"not null;default:'developer'"`
}

// Session represents an authenticated web UI session.
type Session struct {
	ID        uint      `gorm:"primaryKey"`
	UserID    uint      `gorm:"not null;index"`
	TokenHash string    `gorm:"not null"`
	ExpiresAt time.Time `gorm:"not null"`
}

// Shem represents a registered Shem worker.
type Shem struct {
	ID   uint   `gorm:"primaryKey"`
	Name string `gorm:"uniqueIndex;not null"`
	// json:"-" — GET /api/shems serializes this struct straight to the
	// dashboard, and there is no reason to ship credential material to a
	// browser. It is a bcrypt hash of 32 random bytes, so exposure was not
	// exploitable, but it is still the secret's only stored form.
	APIKeyHash    string `gorm:"not null" json:"-"`
	Repos         string `gorm:"not null"` // JSON: []string of normalized URLs
	LastHeartbeat *time.Time
	Status        string  `gorm:"not null;default:'offline'"` // online | offline
	CurrentTicket *string // UUID of the ticket currently being worked on
	// Backend is the adapter name this shem reported.
	Backend string `gorm:"not null;default:''" json:"backend"`
	// Catalog is the model catalog this shem reported, as JSON.
	Catalog string `gorm:"not null;default:'{}'" json:"catalog"`
}

// Ticket represents a work ticket assigned to a Shem.
type Ticket struct {
	ID              string  `gorm:"primaryKey" json:"id"`
	RepoRemote      string  `gorm:"not null;index;uniqueIndex:idx_repo_issue" json:"repo_remote"`
	Title           string  `gorm:"not null;default:''" json:"title"`
	BaseBranch      string  `gorm:"not null;default:'main'" json:"base_branch"`
	Branch          string  `gorm:"not null" json:"branch"`
	Description     string  `gorm:"not null" json:"description"`
	Phase           string  `gorm:"not null;default:'unassigned'" json:"phase"`
	AssignedShem    *uint   `gorm:"index" json:"assigned_shem"`
	CreatedByUserID *uint   `gorm:"index" json:"created_by_user_id"`
	CheckpointPhase *string `json:"checkpoint_phase"`
	CheckpointSHA   *string `json:"checkpoint_sha"`
	IssueNumber     *int    `gorm:"uniqueIndex:idx_repo_issue" json:"issue_number"`
	IssueURL        string  `json:"issue_url"`
	// IntakeApproved records that a human released this externally-sourced
	// ticket from pending-approval via actionStart — the ONLY writer of this
	// column. It is provenance, not phase: no sequence of phase transitions
	// (close, needs-attention, requeue, ...) can flip it, so claimability
	// (spec Amendment 1) is enforced at the claim predicate regardless of
	// how many phase writers exist now or get added later. Tickets created
	// through the web form (nil IssueNumber) are unaffected by construction
	// — see the claim/available predicates in api/tickets.go.
	IntakeApproved bool `gorm:"not null;default:false" json:"intake_approved"`
	// ManualApproval makes the shem wait for a human to approve the spec and
	// the plan. False keeps automatic validation as the only gate.
	ManualApproval bool `gorm:"not null;default:false" json:"manual_approval"`
	// ApprovedBodyHash is the hex-encoded SHA-256 hash of Description at the
	// moment actionStart set IntakeApproved, binding the approval to the
	// exact text a human reviewed (spec Amendment 1 fix round 4). Without
	// this, "approved" meant only "a human pressed Approve on this ticket
	// at some point", not "on this text" — ghsync overwrites Description
	// from the live issue on every poll, so an edit after approval would
	// otherwise reach a shem under an approval that was never given for
	// that text. ghsync.applyIssue re-gates (clears both fields, moves the
	// ticket back to pending-approval) when this no longer matches an
	// unclaimed ticket's incoming issue text.
	ApprovedBodyHash string `gorm:"not null;default:''" json:"approved_body_hash"`
	// BodyHash is the hex-encoded SHA-256 hash of the CURRENT Description,
	// written everywhere Description is written (createTicketFromIssue and
	// ghsync.applyIssue) — fix round 5. The name is historical: Description
	// is the issue's title AND body composed together
	// (github.Issue.TicketDescription), and this hashes all of it, because
	// every byte of it reaches an agent prompt. See ghsync.HashDescription.
	// The claim-adjacent predicates in
	// api/tickets.go (ClaimTicket, availableTickets, resumableTickets)
	// require ApprovedBodyHash to be non-empty AND equal to BodyHash, not
	// merely IntakeApproved (the non-empty half is re-review finding F1: a
	// pre-body_hash database migrates to two empty strings, which compare
	// equal and would otherwise read as an approval): a
	// ticket that is claimed while approved deliberately keeps
	// IntakeApproved=true and its now-stale ApprovedBodyHash if the issue is
	// edited afterward (the running shem is not yanked), but BodyHash keeps
	// moving to match the live issue. That mismatch is what makes the
	// ticket unclaimable again the moment it returns to the pool by ANY
	// path — requeue, the heartbeat reaper, or one none of these fix rounds
	// anticipated — without needing to guard each such path individually.
	BodyHash string `gorm:"not null;default:''" json:"body_hash"`
	// LabelPhase and LabelSeq record the last golem:<phase> label write
	// Golem queued for this ticket, and are the only writers of the
	// transition ordinal in ghsync.LabelKey (finding I4). LabelSeq advances
	// by one whenever a phase transition queues a label for a phase other
	// than LabelPhase; re-sending the SAME transition leaves both unchanged,
	// so it recomputes the same idempotency key and is de-duplicated exactly
	// as before. Without the ordinal the key was per (ticket, phase), which
	// silently suppressed the second lap of a cycle such as
	// implement -> ready-for-review -> implement and left the public issue
	// advertising a phase the ticket had already left. Both are written only
	// by enqueueGitHubPhase, inside the same transaction as the phase change
	// and the outbox insert they describe.
	LabelPhase string `gorm:"not null;default:''" json:"label_phase"`
	LabelSeq   uint   `gorm:"not null;default:0" json:"label_seq"`
	// PRBody is the pull request description the shem generated from the
	// branch's own diff, sent with the branch-pushed report. Empty when
	// generation failed or was not attempted, in which case
	// enqueuePRIfReady falls back to the minimal body — a missing
	// description is an inconvenience, a missing pull request is lost work.
	PRBody   string `gorm:"not null;default:''" json:"-"`
	PRNumber *int   `json:"pr_number"`
	PRURL    string `json:"pr_url"`

	// EpicID/EpicNodeKey/EpicPlanRevision mark the ticket as the
	// materialized child of one approved plan node. NULL/empty on ordinary
	// tickets — the claim predicates treat that as "not epic work", so
	// pre-epic databases migrate forward untouched.
	EpicID           *string `gorm:"index;uniqueIndex:idx_ticket_epic_node" json:"epic_id,omitempty"`
	EpicNodeKey      string  `gorm:"not null;default:'';uniqueIndex:idx_ticket_epic_node" json:"epic_node_key,omitempty"`
	EpicPlanRevision int     `gorm:"not null;default:0" json:"epic_plan_revision,omitempty"`
	// EpicReady is the orchestration-readiness flag: every hard predecessor
	// has an integration receipt. Deliberately separate from Phase (the
	// design: a not-ready child can be unassigned yet dependency-blocked).
	// Maintained on start and every receipt; the claim predicates still
	// re-check epic state and approved revision independently.
	EpicReady bool `gorm:"not null;default:false" json:"epic_ready,omitempty"`
	// PRFixAttempts counts how many times the pull-request monitor has sent
	// this ticket back to revising to fix a failing check or a merge
	// conflict. It is compared against github.MaxPRFixAttempts and is what
	// stops a failure the agent cannot fix from looping forever.
	//
	// Not reset when the pull request goes green: the budget is per ticket,
	// not per problem. A ticket that needed three attempts and then broke
	// again has already shown it is not converging, and a human should see
	// it. A requeue clears it, because that is a human deciding to start
	// the work over.
	PRFixAttempts int `gorm:"not null;default:0" json:"pr_fix_attempts"`
	// PRLastState is a fingerprint of what the pull-request monitor saw on
	// its last pass. It exists so the monitor can write to the activity log
	// when something CHANGES and stay silent otherwise: it runs every couple
	// of minutes for as long as the pull request is open, so anything
	// written per pass is written forever and buries the entries that matter.
	PRLastState string `gorm:"not null;default:''" json:"pr_last_state"`
	// PRDispatchedSHA is the head commit the last automatic fix was
	// dispatched for. A failure reported against a commit already acted on
	// is the SAME failure — CI has not re-run yet — and dispatching again
	// spends an attempt on work already in flight.
	PRDispatchedSHA string `gorm:"not null;default:''" json:"pr_dispatched_sha"`
	// StoppedFromPhase is the phase a human interrupted, remembered so that
	// starting the ticket again puts it back where it was rather than
	// somewhere a rule had to guess.
	StoppedFromPhase string `gorm:"not null;default:''" json:"stopped_from_phase"`
	// Models is the ticket's stage -> model id or "tier:<label>" selections,
	// as JSON.
	Models string `gorm:"not null;default:'{}'" json:"models"`
	// ModelBackend is the backend its model ids were validated against;
	// empty means any backend.
	ModelBackend string `gorm:"not null;default:'';index" json:"model_backend"`
	// ReapedFromShem and ReapedFromPhase record which shem the heartbeat
	// reaper released this ticket from, and the phase it was in. A missed
	// heartbeat does not mean the shem is dead — a stalled database delays
	// heartbeats just as well — and a shem still running the ticket uses
	// these to take it back (api reclaim) instead of finishing into
	// "ticket not owned" and redoing the phase. Cleared by any other phase
	// change (see BeforeUpdate).
	ReapedFromShem  *uint     `gorm:"index" json:"-"`
	ReapedFromPhase string    `gorm:"not null;default:''" json:"-"`
	BranchPushed    bool      `gorm:"not null;default:false" json:"branch_pushed"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}

func (s Shem) RepoList() []string {
	var repos []string
	json.Unmarshal([]byte(s.Repos), &repos) //nolint:errcheck
	return repos
}

// ModelCatalog is the catalog this shem reported, empty if it reported none.
func (s Shem) ModelCatalog() models.Catalog {
	var c models.Catalog
	json.Unmarshal([]byte(s.Catalog), &c) //nolint:errcheck
	return c
}

// ModelSelections is the ticket's selections, empty if it has none.
func (t Ticket) ModelSelections() models.Selections {
	sel := models.Selections{}
	json.Unmarshal([]byte(t.Models), &sel) //nolint:errcheck
	if sel == nil {
		return models.Selections{}
	}
	return sel
}

func (t *Ticket) SetModelSelections(s models.Selections) {
	data, err := json.Marshal(s)
	if err != nil {
		return
	}
	t.Models = string(data)
}

// BeforeCreate generates a UUID for the ticket ID if not already set.
func (t *Ticket) BeforeCreate(tx *gorm.DB) error {
	if t.ID == "" {
		t.ID = uuid.New().String()
	}
	return nil
}

// BeforeUpdate ends a ticket's reaped record on any phase change that does
// not set the record itself. The record says "this ticket was released from
// shem N and has sat in the pool untouched since", which is what makes it
// safe for that shem to reclaim. Once the phase moves — an issue edit
// re-gating it to pending-approval, a human stop, a close — that is no
// longer true, and a later re-approval or resume must not hand it back to a
// run working from the old text or told to quit. Clearing it per call site
// missed two of them in review, so it lives here, on every phase write.
func (t *Ticket) BeforeUpdate(tx *gorm.DB) error {
	if tx.Statement.Changed("Phase") && !tx.Statement.Changed("ReapedFromShem") {
		tx.Statement.SetColumn("ReapedFromShem", nil)
		tx.Statement.SetColumn("ReapedFromPhase", "")
	}
	return nil
}

// CreatorNames resolves CreatedByUserID -> username for a set of tickets in
// a single query. Tickets with a nil CreatedByUserID are simply absent from
// the returned map.
func CreatorNames(gdb *gorm.DB, tickets []Ticket) map[uint]string {
	idSet := make(map[uint]struct{})
	for _, t := range tickets {
		if t.CreatedByUserID != nil {
			idSet[*t.CreatedByUserID] = struct{}{}
		}
	}
	if len(idSet) == 0 {
		return map[uint]string{}
	}
	ids := make([]uint, 0, len(idSet))
	for id := range idSet {
		ids = append(ids, id)
	}
	var users []User
	gdb.Where("id IN ?", ids).Find(&users)
	names := make(map[uint]string, len(users))
	for _, u := range users {
		names[u.ID] = u.Username
	}
	return names
}

// LogEntry represents a single log message from a Shem for a ticket.
type LogEntry struct {
	ID          uint      `gorm:"primaryKey" json:"id"`
	TicketID    string    `gorm:"not null;index;uniqueIndex:idx_ticket_seq" json:"ticket_id"`
	SequenceNum uint      `gorm:"not null;uniqueIndex:idx_ticket_seq" json:"sequence_num"`
	EntryType   string    `gorm:"not null" json:"entry_type"`
	FromRole    string    `json:"from_role"`
	ToRole      string    `json:"to_role"`
	Message     string    `gorm:"not null" json:"message"`
	Model       string    `json:"model"`
	Backend     string    `json:"backend"`
	CreatedAt   time.Time `json:"created_at"`
}

// HumanInput represents a pending or resolved human-input request for a ticket.
type HumanInput struct {
	ID         uint       `gorm:"primaryKey" json:"id"`
	TicketID   string     `gorm:"not null;index" json:"ticket_id"`
	Kind       string     `gorm:"not null" json:"kind"` // question_answer | approval | blocker_ack
	Prompt     string     `gorm:"not null" json:"prompt"`
	Response   *string    `json:"response"`
	CreatedAt  time.Time  `json:"created_at"`
	ResolvedAt *time.Time `json:"resolved_at"`
}

// GitHubRepo holds per-repository issue-sync settings, managed from the
// dashboard. One row per repository Golem may sync.
type GitHubRepo struct {
	ID             uint       `gorm:"primaryKey" json:"id"`
	RepoRemote     string     `gorm:"uniqueIndex;not null" json:"repo_remote"` // normalized via urlnorm
	Owner          string     `gorm:"not null" json:"owner"`
	Name           string     `gorm:"not null" json:"name"`
	Enabled        bool       `gorm:"not null;default:false" json:"enabled"`
	Label          string     `gorm:"not null;default:'golem'" json:"label"`
	LastIssueSync  *time.Time `json:"last_issue_sync"` // `since` cursor
	LastPolledAt   *time.Time `json:"last_polled_at"`
	LastManualSync *time.Time `json:"last_manual_sync"`
	ETag           string     `gorm:"column:etag" json:"-"`
	LastError      string     `json:"last_error"`

	// Graph-build state, recorded rather than a live stream: a build over a
	// large repository takes minutes, and there is no ticket to stream into.
	//
	// No state column. "Building" is started-and-not-finished, "built" is
	// finished with no error, "failed" is finished with one — all derived, so
	// there is no enum that can disagree with the timestamps beside it.
	GraphBuildStartedAt  *time.Time `json:"graph_build_started_at"`
	GraphBuildFinishedAt *time.Time `json:"graph_build_finished_at"`
	GraphBuildError      string     `json:"graph_build_error"`
}

// GraphBuildStale reports whether a build has been running longer than is
// plausible, which in practice means the shem died holding it.
//
// Without this a crashed build leaves the row reading "building…" forever and
// the button disabled forever — the same shape as an outbox row that parks
// with nothing able to un-park it. The UI offers the button again past this
// point rather than requiring someone to edit the database.
func (r GitHubRepo) GraphBuildStale(now time.Time) bool {
	if r.GraphBuildStartedAt == nil {
		return false
	}
	if r.graphBuildFinished() {
		return false
	}
	return now.Sub(*r.GraphBuildStartedAt) > GraphBuildTimeout
}

// GraphBuildRunning reports whether a build is in flight and not yet stale.
func (r GitHubRepo) GraphBuildRunning(now time.Time) bool {
	if r.GraphBuildStartedAt == nil {
		return false
	}
	if r.graphBuildFinished() {
		return false
	}
	return !r.GraphBuildStale(now)
}

// graphBuildFinished reports whether the last build reported back. Uses >=,
// not >, so a build that finishes in the same clock tick it started counts as
// finished — matching the SQL claim predicate.
func (r GitHubRepo) graphBuildFinished() bool {
	return r.GraphBuildFinishedAt != nil && !r.GraphBuildFinishedAt.Before(*r.GraphBuildStartedAt)
}

// GraphBuildTimeout bounds how long a build is believed to still be running.
// Generous: a first build over a thousand modules involves an agent call per
// batch, and calling a slow build dead is worse than waiting.
const GraphBuildTimeout = 45 * time.Minute

// GitHubOutbox is a pending write to GitHub. Rows are inserted in the same
// transaction as the ticket change that caused them, and drained by the
// ghsync worker.
//
// IdempotencyKey is a deterministic string derived from the event (for
// example "<ticketID>:comment:plan-approved"). The unique index on it is what
// makes re-enqueuing after a crash safe: the duplicate insert fails and the
// caller treats that specific failure as success.
type GitHubOutbox struct {
	ID             uint       `gorm:"primaryKey" json:"id"`
	TicketID       string     `gorm:"not null;index" json:"ticket_id"`
	Kind           string     `gorm:"not null" json:"kind"` // comment | label | pr | close_issue
	Payload        string     `gorm:"not null" json:"payload"`
	IdempotencyKey string     `gorm:"uniqueIndex;not null" json:"idempotency_key"`
	Attempts       int        `gorm:"not null;default:0" json:"attempts"`
	NextAttempt    time.Time  `gorm:"index" json:"next_attempt"`
	LastError      string     `json:"last_error"`
	DoneAt         *time.Time `json:"done_at"`
}

// Epic is one spec-driven run: an original spec, immutable plan revisions,
// and a state machine over them. Children are ordinary tickets (linked
// from EpicNode once materialized), so the existing dashboard and worker
// fleet keep working unchanged.
type Epic struct {
	ID           string `gorm:"primaryKey" json:"id"`
	RepoRemote   string `gorm:"not null;index" json:"repo_remote"`
	Title        string `gorm:"not null" json:"title"`
	SpecMarkdown string `gorm:"not null" json:"spec_markdown"` // the original spec, kept verbatim
	SpecHash     string `gorm:"not null" json:"spec_hash"`
	BaseBranch   string `gorm:"not null;default:'main'" json:"base_branch"`
	BaseSHA      string `gorm:"not null;default:''" json:"base_sha"` // resolved when the run starts
	// BranchStrategy is "epic-branch" (default: children integrate into an
	// epic integration branch) or "trunk-flags" (independently safe slices
	// integrate directly to base). Never changed mid-run.
	BranchStrategy string `gorm:"not null;default:'epic-branch'" json:"branch_strategy"`
	// IntegrationRef is where children merge for the epic-branch strategy
	// (epic/<id>). Empty under trunk-flags: slices target base.
	IntegrationRef string `gorm:"not null;default:''" json:"integration_ref"`
	// DeliveryTarget is "feature-branch" (default: the assembled branch
	// gets a final PR and the human merges) or "base-branch" (opt-in
	// auto-merge after a current tester pass). Under trunk-flags only
	// base-branch is meaningful.
	DeliveryTarget string `gorm:"not null;default:'feature-branch'" json:"delivery_target"`
	// AutoMergeChildren allows the integration lane to merge child PRs
	// without a human click, once every gate (reviewer verdict, checks,
	// tester verdict) passes. AutoMergeFinal allows the final PR to
	// merge to base; it is only meaningful with the base-branch
	// delivery target. TesterRequired is stored, not computed: setting
	// any auto-merge forces it true, and it can never be cleared while
	// auto-merge is on. Defaults stay off everywhere — pre-existing
	// rows keep their human-merged behavior untouched.
	AutoMergeChildren bool `gorm:"not null;default:false" json:"auto_merge_children"`
	AutoMergeFinal    bool `gorm:"not null;default:false" json:"auto_merge_final"`
	TesterRequired    bool `gorm:"not null;default:false" json:"tester_required"`
	// AutoApprovePhases is the run-level phase auto-approval policy:
	// children materialize with ManualApproval = !AutoApprovePhases. The
	// policy is a snapshot taken at Start — editing the epic afterwards
	// never changes a child that already exists, so a claimed child's
	// mode is never silently changed. A child keeps its own
	// ManualApproval as the per-ticket override.
	AutoApprovePhases bool `gorm:"not null;default:false" json:"auto_approve_phases"`
	// State: draft, planning, awaiting-approval, running, paused,
	// needs-attention, awaiting-final-review, completed, cancelled.
	State string `gorm:"not null;default:'draft'" json:"state"`
	// CurrentRevision is the latest plan revision; ApprovedRevision and
	// ApprovedPlanHash bind the human's approval to that exact revision's
	// content. Adding a revision clears both — editing approved content
	// invalidates the approval.
	CurrentRevision  int    `gorm:"not null;default:0" json:"current_revision"`
	ApprovedRevision int    `gorm:"not null;default:0" json:"approved_revision"`
	ApprovedPlanHash string `gorm:"not null;default:''" json:"approved_plan_hash"`
	CreatedByUserID  *uint  `gorm:"index" json:"created_by_user_id"`
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

// EpicPlanRevision is immutable: once written it never changes. An edit is
// a new revision with a new hash.
type EpicPlanRevision struct {
	ID         uint       `gorm:"primaryKey" json:"id"`
	EpicID     string     `gorm:"not null;index;uniqueIndex:idx_epic_revision" json:"epic_id"`
	Revision   int        `gorm:"not null;uniqueIndex:idx_epic_revision" json:"revision"`
	SpecHash   string     `gorm:"not null" json:"spec_hash"`
	RepoSHA    string     `gorm:"not null;default:''" json:"repo_sha"` // the commit the planner read
	PlanJSON   string     `gorm:"not null" json:"plan_json"`           // canonical epic.Plan
	PlanHash   string     `gorm:"not null" json:"plan_hash"`
	ApprovedBy string     `gorm:"not null;default:''" json:"approved_by"`
	ApprovedAt *time.Time `json:"approved_at"`
	CreatedAt  time.Time
}

// EpicNode is one plan node's membership snapshot in a revision. TicketID
// and IntegratedSHA stay empty until execution (M2/M3): a node is planning
// data here, and integration receipts are what readiness counts.
type EpicNode struct {
	ID             uint   `gorm:"primaryKey" json:"id"`
	EpicID         string `gorm:"not null;index;uniqueIndex:idx_epic_node_rev" json:"epic_id"`
	Revision       int    `gorm:"not null;uniqueIndex:idx_epic_node_rev" json:"revision"`
	NodeKey        string `gorm:"not null;uniqueIndex:idx_epic_node_rev" json:"node_key"`
	Title          string `gorm:"not null" json:"title"`
	RequirementIDs string `gorm:"not null;default:'[]'" json:"requirement_ids"` // JSON array
	BranchTarget   string `gorm:"not null" json:"branch_target"`
	TicketID       string `gorm:"not null;default:'';index" json:"ticket_id"`
	// The integration receipt: the node merged into its branch target.
	// IntegratedSHA is the merged head; IntegratedRef is where it landed.
	// Empty until the receipt exists — ready-for-review is NOT integration.
	IntegratedSHA string     `gorm:"not null;default:''" json:"integrated_sha"`
	IntegratedRef string     `gorm:"not null;default:''" json:"integrated_ref"`
	IntegratedAt  *time.Time `json:"integrated_at,omitempty"`
}

// EpicEdge is one hard dependency in a revision. No soft/overlap rows:
// those are resource constraints, not dependencies.
type EpicEdge struct {
	ID          uint   `gorm:"primaryKey" json:"id"`
	EpicID      string `gorm:"not null;index" json:"epic_id"`
	Revision    int    `gorm:"not null" json:"revision"`
	Predecessor string `gorm:"not null" json:"predecessor"`
	Successor   string `gorm:"not null" json:"successor"`
	Reason      string `gorm:"not null" json:"reason"`
}

// IntegrationAttempt is the structured evidence of one integration-lane
// merge attempt against a child pull request. The lane writes a row before
// it calls GitHub and resolves the same row after, so a crash between the
// two leaves a row in "merging" state — the reconcile pass reads exactly
// that row and checks the remote instead of guessing. The per-head
// deduplication index is what bounds conflict-fix dispatch and stale-head
// retries to one recorded attempt per observed head.
type IntegrationAttempt struct {
	ID          uint   `gorm:"primaryKey" json:"id"`
	EpicID      string `gorm:"not null;index;uniqueIndex:idx_attempt_node_head" json:"epic_id"`
	EpicNodeKey string `gorm:"not null;uniqueIndex:idx_attempt_node_head" json:"epic_node_key"`
	TicketID    string `gorm:"not null;index" json:"ticket_id"`
	PRNumber    int    `gorm:"not null;default:0" json:"pr_number"`
	// HeadSHA and BaseSHA are what the lane validated. HeadSHA is part of
	// the dedupe key: a new head is new work and earns its own attempt,
	// while the same head retried is one attempt.
	HeadSHA string `gorm:"not null;uniqueIndex:idx_attempt_node_head" json:"head_sha"`
	BaseSHA string `gorm:"not null;default:''" json:"base_sha"`
	// State: merging (call in flight or lost to a crash — the reconcile
	// pass owns these), merged, failed-stale, failed-forbidden,
	// failed-conflict, failed-unknown, blocked-tester (policy requires a
	// tester verdict this head does not have). Failed and blocked states
	// carry Cause with the named reason; the lane fails closed, so an
	// unnamed cause is a bug.
	State string `gorm:"not null;default:'merging'" json:"state"`
	Cause string `gorm:"not null;default:''" json:"cause"`
	// MergeSHA is the merge commit GitHub returned — what the epic node
	// receipt must name, since it is what the target ref contains.
	MergeSHA  string `gorm:"not null;default:''" json:"merge_sha"`
	CreatedAt time.Time
	UpdatedAt time.Time
}

// TesterVerdict is the tester agent's structured verdict for one merge
// candidate: the scenarios it ran against the exact head, and pass, fail
// or inconclusive. Only a pass whose HeadSHA still matches the candidate
// authorizes an auto-merge — a fail or inconclusive blocks, and a verdict
// for a different head is stale evidence about code that no longer
// exists, so it is ignored rather than honored. BaseSHA is evidence of
// what the candidate was tested against; the lane matches on the head.
type TesterVerdict struct {
	ID          uint   `gorm:"primaryKey" json:"id"`
	EpicID      string `gorm:"not null;index" json:"epic_id"`
	EpicNodeKey string `gorm:"not null;default:'';index" json:"epic_node_key"` // empty = final PR
	HeadSHA     string `gorm:"not null" json:"head_sha"`
	BaseSHA     string `gorm:"not null;default:''" json:"base_sha"`
	Verdict     string `gorm:"not null" json:"verdict"`              // pass, fail, inconclusive
	Scenarios   string `gorm:"not null;default:''" json:"scenarios"` // what ran, one per line
	Evidence    string `gorm:"not null;default:''" json:"evidence"`  // log tail / failure detail
	Backend     string `gorm:"not null;default:''" json:"backend"`   // which configured backend ran it
	CreatedAt   time.Time
}
