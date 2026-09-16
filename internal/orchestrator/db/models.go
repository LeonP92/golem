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
	ID            uint   `gorm:"primaryKey"`
	Name          string `gorm:"uniqueIndex;not null"`
	APIKeyHash    string `gorm:"not null"`
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
	IntakeApproved bool      `gorm:"not null;default:false" json:"intake_approved"`
	PRNumber       *int      `json:"pr_number"`
	PRURL          string    `json:"pr_url"`
	BranchPushed   bool      `gorm:"not null;default:false" json:"branch_pushed"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
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
