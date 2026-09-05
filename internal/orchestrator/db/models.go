package db

import (
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
	Branch          string    `gorm:"not null" json:"branch"`
	Description     string    `gorm:"not null" json:"description"`
	Phase           string    `gorm:"not null;default:'unassigned'" json:"phase"`
	AssignedShem    *uint     `gorm:"index" json:"assigned_shem"`
	CheckpointPhase *string   `json:"checkpoint_phase"`
	CheckpointSHA   *string   `json:"checkpoint_sha"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}

// BeforeCreate generates a UUID for the ticket ID if not already set.
func (t *Ticket) BeforeCreate(tx *gorm.DB) error {
	if t.ID == "" {
		t.ID = uuid.New().String()
	}
	return nil
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
