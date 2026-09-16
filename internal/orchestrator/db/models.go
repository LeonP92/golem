package db

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
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
	LabelPhase   string    `gorm:"not null;default:''" json:"label_phase"`
	LabelSeq     uint      `gorm:"not null;default:0" json:"label_seq"`
	PRNumber     *int      `json:"pr_number"`
	PRURL        string    `json:"pr_url"`
	BranchPushed bool      `gorm:"not null;default:false" json:"branch_pushed"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

func (s Shem) RepoList() []string {
	var repos []string
	json.Unmarshal([]byte(s.Repos), &repos) //nolint:errcheck
	return repos
}

// BeforeCreate generates a UUID for the ticket ID if not already set.
func (t *Ticket) BeforeCreate(tx *gorm.DB) error {
	if t.ID == "" {
		t.ID = uuid.New().String()
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
}

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
