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
	ID            uint       `gorm:"primaryKey"`
	Name          string     `gorm:"uniqueIndex;not null"`
	APIKeyHash    string     `gorm:"not null"`
	Repos         string     `gorm:"not null"` // JSON: []string of normalized URLs
	LastHeartbeat *time.Time
	Status        string  `gorm:"not null;default:'offline'"` // online | offline
	CurrentTicket *string // UUID of the ticket currently being worked on
}

// Ticket represents a work ticket assigned to a Shem.
type Ticket struct {
	ID              string    `gorm:"primaryKey" json:"id"`
	RepoRemote      string    `gorm:"not null;index" json:"repo_remote"`
	Title           string    `gorm:"not null;default:''" json:"title"`
	BaseBranch      string    `gorm:"not null;default:'main'" json:"base_branch"`
	Branch          string    `gorm:"not null" json:"branch"`
	Description     string    `gorm:"not null" json:"description"`
	Phase           string    `gorm:"not null;default:'unassigned'" json:"phase"`
	AssignedShem    *uint     `gorm:"index" json:"assigned_shem"`
	CreatedByUserID *uint     `gorm:"index" json:"created_by_user_id"`
	CheckpointPhase *string   `json:"checkpoint_phase"`
	CheckpointSHA   *string   `json:"checkpoint_sha"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
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
