package client

import (
	"encoding/json"
	"fmt"
	"net/http"
)

type AttentionTicket struct {
	TicketID   string `json:"ticket_id"`
	RepoRemote string `json:"repo_remote"`
}

type AttentionClaim struct {
	Claim   *ClaimResponse `json:"claim"`
	Token   string         `json:"token"`
	Reason  string         `json:"reason"`
	Attempt int            `json:"attempt"`
}

type AttentionResult struct {
	Token    string `json:"token"`
	Retry    bool   `json:"retry"`
	Feedback string `json:"feedback"`
	Reason   string `json:"reason"`
}

func (c *Client) GetAttention() ([]AttentionTicket, error) {
	resp, err := c.do(http.MethodGet, "/api/tickets/attention", nil)
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
