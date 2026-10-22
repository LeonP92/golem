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
	// SupportsEpics is cleared when a pre-epic worker registers.
	SupportsEpics bool `gorm:"not null;default:false" json:"supports_epics"`
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
	// RetryPhase records the failed worker phase for an explicit checkpoint
	// retry (human or automatic). Requeue clears it and starts over.
	RetryPhase string `gorm:"not null;default:''" json:"-"`
	// Recovery is opt-in by the failure source, not by the badge. Old rows,
	// human parks and exhausted PR/epic retries remain human-owned.
	AttentionEligible    bool       `gorm:"not null;default:false" json:"attention_eligible"`
	AttentionReason      string     `gorm:"not null;default:''" json:"attention_reason"`
	AttentionAttempts    int        `gorm:"not null;default:0" json:"attention_attempts"`
	AttentionFingerprint string     `gorm:"not null;default:''" json:"-"`
	AttentionRepeats     int        `gorm:"not null;default:0" json:"-"`
	AttentionToken       string     `gorm:"not null;default:''" json:"-"`
	AttentionLeaseUntil  *time.Time `json:"-"`
	IssueNumber          *int       `gorm:"uniqueIndex:idx_repo_issue" json:"issue_number"`
	IssueURL             string     `json:"issue_url"`
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
	// TesterRequired runs the tester stage on the pushed head and records a
	// structured verdict on the ticket. This is the ticket-level primitive:
	// any ticket can ask for it, and an epic run composes it by setting the
	// flag on every child at materialization. Default false keeps every
	// pre-existing ticket on its old path.
	TesterRequired bool `gorm:"not null;default:false" json:"tester_required"`
	// VerifyDisabled opts the ticket out of the worker's self-verification
	// (`golem ticket verify` before the review gate). Verification is the
	// default for every ticket; a pre-existing ticket migrates forward
	// with it on.
	VerifyDisabled bool `gorm:"not null;default:false" json:"verify_disabled"`
	// AskDecisions sends open product and design questions to a human. Off
	// (the default, so every pre-existing ticket too), agents decide them
	// from DecisionGuidance, record each as a DECISION entry and carry on.
	// Empty DecisionGuidance means the built-in default.
	AskDecisions     bool   `gorm:"not null;default:false" json:"ask_decisions"`
	DecisionGuidance string `gorm:"not null;default:''" json:"decision_guidance"`
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

	// Epic child fields: the run, plan node and revision this ticket was
	// materialized from. NULL/empty on ordinary tickets.
	EpicID           *string `gorm:"index;uniqueIndex:idx_ticket_epic_node" json:"epic_id,omitempty"`
	EpicNodeKey      string  `gorm:"not null;default:'';uniqueIndex:idx_ticket_epic_node" json:"epic_node_key,omitempty"`
	EpicPlanRevision int     `gorm:"not null;default:0" json:"epic_plan_revision,omitempty"`
	// EpicReady: every hard predecessor is integrated. Separate from Phase.
	EpicReady bool `gorm:"not null;default:false" json:"epic_ready,omitempty"`
	// EpicIntegratedSHA is the integration receipt: the merge commit that
	// landed this node on the run's target ref. It, not Phase, unblocks
	// successors.
	EpicIntegratedSHA string `gorm:"not null;default:''" json:"epic_integrated_sha,omitempty"`
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

// PhaseUpdates builds the common fields for an orchestrator phase transition.
// A needs-attention transition remembers the previous active phase atomically;
// every other transition clears the retry hint so stale phases cannot leak
// into a later run.
func PhaseUpdates(phase string) map[string]any {
	updates := map[string]any{"phase": phase, "attention_eligible": false, "attention_token": "", "attention_lease_until": nil}
	if phase == "needs-attention" {
		updates["retry_phase"] = gorm.Expr("CASE WHEN phase = ? THEN retry_phase ELSE phase END", phase)
	} else {
		updates["retry_phase"] = ""
	}
	return updates
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
	ID uint `gorm:"primaryKey" json:"id"`
	// TicketID scopes a ticket write; EpicID scopes a run-level write (the
	// final pull request). Exactly one of the two is set.
	TicketID       string     `gorm:"not null;index" json:"ticket_id"`
	EpicID         string     `gorm:"not null;default:'';index" json:"epic_id"`
	Kind           string     `gorm:"not null" json:"kind"` // comment | label | pr | close_issue
	Payload        string     `gorm:"not null" json:"payload"`
	IdempotencyKey string     `gorm:"uniqueIndex;not null" json:"idempotency_key"`
	Attempts       int        `gorm:"not null;default:0" json:"attempts"`
	NextAttempt    time.Time  `gorm:"index" json:"next_attempt"`
	LastError      string     `json:"last_error"`
	DoneAt         *time.Time `json:"done_at"`
}

// Epic is one spec-driven run: the original spec, immutable plan
// revisions, and the run state. Its children are ordinary tickets.
type Epic struct {
	ID           string `gorm:"primaryKey" json:"id"`
	RepoRemote   string `gorm:"not null;index" json:"repo_remote"`
	Title        string `gorm:"not null" json:"title"`
	SpecMarkdown string `gorm:"not null" json:"spec_markdown"`
	SpecHash     string `gorm:"not null" json:"spec_hash"`
	BaseBranch   string `gorm:"not null;default:'main'" json:"base_branch"`
	BaseSHA      string `gorm:"not null;default:''" json:"base_sha"`
	// BranchStrategy: "epic-branch" (children merge into IntegrationRef,
	// epic/<id>) or "trunk-flags" (children merge into BaseBranch).
	BranchStrategy string `gorm:"not null;default:'epic-branch'" json:"branch_strategy"`
	IntegrationRef string `gorm:"not null;default:''" json:"integration_ref"`
	// DeliveryTarget records the requested output. Epic-branch runs require
	// a manual final merge; trunk-flags already integrates into base.
	DeliveryTarget string `gorm:"not null;default:'feature-branch'" json:"delivery_target"`
	// Auto-merging children forces TesterRequired on (set at create).
	AutoMergeChildren bool `gorm:"not null;default:false" json:"auto_merge_children"`
	TesterRequired    bool `gorm:"not null;default:false" json:"tester_required"`
	// FinalSHA is the base head the human attested to at completion;
	// FinalPRNumber/FinalPRURL record the pull request the GitHub add-on
	// opened for the integration branch, when one was opened.
	FinalSHA      string `gorm:"not null;default:''" json:"final_sha"`
	FinalPRNumber *int   `json:"final_pr_number"`
	FinalPRURL    string `gorm:"not null;default:''" json:"final_pr_url"`
	// AutoApprovePhases is snapshotted into each child's ManualApproval at Start.
	AutoApprovePhases bool `gorm:"not null;default:false" json:"auto_approve_phases"`
	// AskDecisions and DecisionGuidance are snapshotted into each child at Start.
	AskDecisions     bool   `gorm:"not null;default:false" json:"ask_decisions"`
	DecisionGuidance string `gorm:"not null;default:''" json:"decision_guidance"`
	// AutoStartGeneratedPlan is opt-in: a successfully generated and
	// validated plan draft is saved as a revision, approved and started
	// without human clicks. Approval is recorded under the automation's
	// name, not a human's. Off by default: the plan approval gate otherwise
	// stays manual and hash-bound.
	AutoStartGeneratedPlan bool   `gorm:"not null;default:false" json:"auto_start_generated_plan"`
	State                  string `gorm:"not null;default:'draft'" json:"state"` // epics.State* constants
	// ApprovedRevision/ApprovedPlanHash bind approval to exact content; a
	// new revision clears both.
	CurrentRevision  int    `gorm:"not null;default:0" json:"current_revision"`
	ApprovedRevision int    `gorm:"not null;default:0" json:"approved_revision"`
	ApprovedPlanHash string `gorm:"not null;default:''" json:"approved_plan_hash"`
	CreatedByUserID  *uint  `gorm:"index" json:"created_by_user_id"`
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

// EpicPlanRevision is immutable once written; an edit is a new revision.
type EpicPlanRevision struct {
	ID         uint       `gorm:"primaryKey" json:"id"`
	EpicID     string     `gorm:"not null;index;uniqueIndex:idx_epic_revision" json:"epic_id"`
	Revision   int        `gorm:"not null;uniqueIndex:idx_epic_revision" json:"revision"`
	SpecHash   string     `gorm:"not null" json:"spec_hash"`
	RepoSHA    string     `gorm:"not null;default:''" json:"repo_sha"` // the commit the planner read
	PlanJSON   string     `gorm:"not null" json:"plan_json"`           // canonical epic.Plan
	PlanHash   string     `gorm:"not null" json:"plan_hash"`
	ApprovedBy string     `gorm:"not null;default:''" json:"approved_by"` // username
	ApprovedAt *time.Time `json:"approved_at"`
	CreatedAt  time.Time
}

// EpicIntegrationSlot is the per-run integration lease: at most one child
// merges into the run's target ref at a time, so two children finishing
// together cannot interleave their merges. The row is the slot — its
// primary key IS the epic — and a holder that crashes stops blocking the
// lane when ExpiresAt passes; the next gate check takes the slot over.
type EpicIntegrationSlot struct {
	LeaseID   string    `gorm:"not null;default:''" json:"lease_id"`
	EpicID    string    `gorm:"primaryKey" json:"epic_id"`
	NodeKey   string    `gorm:"not null" json:"node_key"`
	HeadSHA   string    `gorm:"not null" json:"head_sha"`
	ShemID    uint      `gorm:"not null" json:"shem_id"`
	ExpiresAt time.Time `gorm:"not null" json:"expires_at"`
	CreatedAt time.Time
	UpdatedAt time.Time
}

// TesterVerdict is the tester's append-only verdict for one merge
// candidate head. Only a pass for the candidate's current head authorizes
// an auto-merge. EpicNodeKey is empty for the final PR.
type TesterVerdict struct {
	ID uint `gorm:"primaryKey" json:"id"`
	// EpicID and EpicNodeKey name the merge candidate for an epic child.
	// TicketID names it for a standalone ticket: the verdict table serves
	// both, because the tester is a ticket-level stage that epics compose,
	// not an epic-only mechanism. Exactly one of the two is set; a row with
	// neither is rejected at the write endpoints.
	EpicID      string `gorm:"not null;index" json:"epic_id"`
	EpicNodeKey string `gorm:"not null;default:'';index" json:"epic_node_key"`
	TicketID    string `gorm:"not null;default:'';index" json:"ticket_id"`
	HeadSHA     string `gorm:"not null" json:"head_sha"`
	BaseSHA     string `gorm:"not null;default:''" json:"base_sha"`
	Verdict     string `gorm:"not null" json:"verdict"` // pass, fail, inconclusive
	Scenarios   string `gorm:"not null;default:''" json:"scenarios"`
	Evidence    string `gorm:"not null;default:''" json:"evidence"`
	Backend     string `gorm:"not null;default:''" json:"backend"`
	CreatedAt   time.Time
}

// EpicPlanJob is one durable plan-generation job for an epic. The
// orchestrator owns the row; a shem claims it (ShemID + ClaimedAt act as
// the lease, refreshed while the session runs) and posts back a draft plan
// or an error. The draft is not a plan revision: it becomes one only when
// a human saves it through the ordinary revision flow (unless the epic set
// AutoStartGeneratedPlan), so by default approval keeps
// binding to content a human saw.
type EpicPlanJob struct {
	ID     string `gorm:"primaryKey" json:"id"`
	EpicID string `gorm:"not null;index" json:"epic_id"`
	// Status: queued -> running -> done | failed. An active job (queued or
	// running) blocks a new one for the same epic — enforced by a partial
	// unique index created in Open.
	Status    string     `gorm:"not null;default:'queued'" json:"status"`
	ShemID    *uint      `json:"shem_id"`
	ClaimedAt *time.Time `json:"claimed_at"`
	// DraftJSON is the canonical plan JSON the shem produced, validated
	// against the epic's target ref before it was stored.
	DraftJSON string `gorm:"not null;default:''" json:"draft_json"`
	// Err names the failure when Status is failed (validator errors or the
	// agent's own output); empty otherwise.
	Err string `gorm:"column:error;not null;default:''" json:"error"`
	// LogTail is the last slice of the agent session's output, for the
	// epic page's live status.
	LogTail string `gorm:"not null;default:''" json:"log_tail"`
	// Attempt counts agent sessions run for this job (first try plus at
	// most one repair), so a reclaimed job does not restart the repair
	// loop forever.
	Attempt   int `gorm:"not null;default:0" json:"attempt"`
	CreatedAt time.Time
	UpdatedAt time.Time
}
