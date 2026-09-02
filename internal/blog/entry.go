package blog

import "time"

type EntryType string

const (
	TypeStatus          EntryType = "STATUS"
	TypeFinding         EntryType = "FINDING"
	TypeBlocker         EntryType = "BLOCKER"
	TypeQuestion        EntryType = "QUESTION"
	TypeAnswer          EntryType = "ANSWER"
	TypeTimeout         EntryType = "TIMEOUT"
	TypeBlockedToolCall EntryType = "BLOCKED_TOOL_CALL"
	TypeResolved        EntryType = "RESOLVED"
)

// Entry is one line of a ticket's append-only blackboard log.
type Entry struct {
	Timestamp time.Time `json:"timestamp"`
	Role      string    `json:"role"`
	Type      EntryType `json:"type"`
	Target    string    `json:"target,omitempty"`
	ID        string    `json:"id,omitempty"`
	InReplyTo string    `json:"in_reply_to,omitempty"`
	Message   string    `json:"message"`
	CommitSHA string    `json:"commit_sha,omitempty"`
	Model     string    `json:"model,omitempty"`
}

func NewEntry(role string, typ EntryType, message string) Entry {
	return Entry{
		Timestamp: time.Now(),
		Role:      role,
		Type:      typ,
		Message:   message,
	}
}
