package client

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/leonp92/golem/internal/evidence"
)

type AttentionTicket struct {
	TicketID   string `json:"id"`
	RepoRemote string `json:"repo_remote"`
	Reason     string `json:"attention_reason"`
}

type AttentionClaim struct {
	Claim   *ClaimResponse `json:"claim"`
	Token   string         `json:"token"`
	Reason  string         `json:"reason"`
	Attempt int            `json:"attempt"`
	// WaitResume marks a claim whose supervisor wait just elapsed: resume the
	// failed step directly instead of diagnosing a stale failure.
	WaitResume bool `json:"wait_resume,omitempty"`
}

// AttentionDecision is the decided route's record. It rides the resolve so
// the decision and the resume it authorizes commit together.
type AttentionDecision struct {
	Question string `json:"question"`
	Choice   string `json:"choice"`
	Reason   string `json:"reason"`
	Basis    string `json:"basis"`
}

// AttentionResult is one recovery outcome: a retry (with optional decision),
// a supervisor wait, or a human park (the zero outcome). The server owns every
// budget and rejects an exhausted one.
type AttentionResult struct {
	Token    string `json:"token"`
	Retry    bool   `json:"retry"`
	Feedback string `json:"feedback"`
	Reason   string `json:"reason"`
	// Wait asks the server to park the ticket on the supervisor's clock:
	// "transient" or "usage_limit". The server sets the delay; for a usage
	// limit ResetAt is the reset time the shem parsed, if any.
	Wait     string             `json:"wait,omitempty"`
	ResetAt  *time.Time         `json:"reset_at,omitempty"`
	Decision *AttentionDecision `json:"decision,omitempty"`
}

func (c *Client) GetAttention() ([]AttentionTicket, error) {
	resp, err := c.do(http.MethodGet, "/api/tickets?attention=true", nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("attention list: status %d", resp.StatusCode)
	}
	var out []AttentionTicket
	err = json.NewDecoder(resp.Body).Decode(&out)
	return out, err
}

func (c *Client) ClaimAttention(id string, max int) (*AttentionClaim, error) {
	resp, err := c.do(http.MethodPost, fmt.Sprintf("/api/tickets/%s/attention-claim", id), map[string]int{"max_attempts": max})
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusConflict {
		return nil, ErrNotAvailable
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("attention claim: status %d", resp.StatusCode)
	}
	var out AttentionClaim
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	if out.Claim == nil || out.Token == "" {
		return nil, fmt.Errorf("attention claim missing ticket or token")
	}
	return &out, nil
}

func (c *Client) ResolveAttention(id string, result AttentionResult) (*ClaimResponse, error) {
	resp, err := c.do(http.MethodPost, fmt.Sprintf("/api/tickets/%s/attention-result", id), result)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusConflict {
		return nil, ErrNotAvailable
	}
	if resp.StatusCode == http.StatusNoContent {
		return nil, nil
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("attention result: status %d", resp.StatusCode)
	}
	var out ClaimResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return &out, nil
}

// PostFailure opts only worker-originated failures into bounded diagnosis.
// Legacy PostPhase("needs-attention") and human parks remain untouched.
func (c *Client) PostFailure(id, reason string) error {
	reason = evidence.Clean(reason)
	resp, err := c.do(http.MethodPatch, fmt.Sprintf("/api/tickets/%s/phase", id), map[string]string{"phase": "needs-attention", "attention_reason": reason})
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusConflict {
		return ErrNotOwner
	}
	if resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("post failure: status %d", resp.StatusCode)
	}
	return nil
}

// BackendLimit returns when this shem's backend is usable again; the zero time
// means it is not limited. The server stores it on the shem row.
func (c *Client) BackendLimit() (time.Time, error) {
	resp, err := c.do(http.MethodGet, "/api/shems/me", nil)
	if err != nil {
		return time.Time{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return time.Time{}, fmt.Errorf("backend limit: status %d", resp.StatusCode)
	}
	var out struct {
		LimitedUntil time.Time `json:"limited_until"`
	}
	err = json.NewDecoder(resp.Body).Decode(&out)
	return out.LimitedUntil, err
}

// ReportBackendLimit records on the server that this shem hit a usage limit
// lifting at until, so a restart still holds.
func (c *Client) ReportBackendLimit(until time.Time) error {
	resp, err := c.do(http.MethodPost, "/api/backend-limit", map[string]any{"limited_until": until})
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("report backend limit: status %d", resp.StatusCode)
	}
	return nil
}

// MigratableTicket is a limit-waiting ticket the server allows this shem to move.
type MigratableTicket struct {
	TicketID      string `json:"id"`
	RepoRemote    string `json:"repo_remote"`
	Branch        string `json:"branch"`
	CheckpointSHA string `json:"checkpoint_sha"`
}

// Migratable lists the tickets this shem may move off itself.
func (c *Client) Migratable() ([]MigratableTicket, error) {
	resp, err := c.do(http.MethodGet, "/api/tickets?migratable=true", nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("migratable: status %d", resp.StatusCode)
	}
	var out []MigratableTicket
	err = json.NewDecoder(resp.Body).Decode(&out)
	return out, err
}

// MigrateTicket moves a limit-waiting ticket to the pool, naming the
// checkpoint the caller verified and, when it captured unfinished work, the
// pushed commit to resume from.
func (c *Client) MigrateTicket(id, sha, captured string) error {
	resp, err := c.do(http.MethodPost, fmt.Sprintf("/api/tickets/%s/migrate", id), map[string]string{"checkpoint_sha": sha, "captured_sha": captured})
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("migrate: status %d", resp.StatusCode)
	}
	return nil
}

// ReleaseTicket hands a claimed, unstarted ticket back to the pool.
func (c *Client) ReleaseTicket(id string) error {
	resp, err := c.do(http.MethodPost, fmt.Sprintf("/api/tickets/%s/release", id), nil)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("release: status %d", resp.StatusCode)
	}
	return nil
}
