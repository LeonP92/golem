package client

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/leonp92/golem/internal/orchestrator/db"
)

// ErrNotAvailable is returned when a ticket claim fails with 409 Conflict.
var ErrNotAvailable = errors.New("ticket not available")

// ErrNotOwner is returned when a phase update is rejected because this shem
// no longer owns the ticket (e.g. after a requeue).
var ErrNotOwner = errors.New("ticket not owned by this shem")

// LogPayload is the request body for logging.
type LogPayload struct {
	EntryType string `json:"entry_type"`
	FromRole  string `json:"from_role"`
	ToRole    string `json:"to_role"`
	Message   string `json:"message"`
	Model     string `json:"model"`
	Backend   string `json:"backend"`
}

// ClaimResponse is the response from claiming a ticket.
type ClaimResponse struct {
	TicketID        string        `json:"ticket_id"`
	Branch          string        `json:"branch"`
	RepoRemote      string        `json:"repo_remote"`
	Description     string        `json:"description"`
	CheckpointPhase *string       `json:"checkpoint_phase"`
	CheckpointSHA   *string       `json:"checkpoint_sha"`
	LogEntries      []db.LogEntry `json:"log_entries"`
}

// PendingInput represents a pending human input.
type PendingInput struct {
	ID        uint      `json:"id"`
	TicketID  string    `json:"ticket_id"`
	Kind      string    `json:"kind"`
	Prompt    string    `json:"prompt"`
	CreatedAt time.Time `json:"created_at"`
}

// Client is the HTTP client for orchestrator API calls.
type Client struct {
	baseURL       string
	apiKey        string
	name          string
	http          *http.Client
	RetryInitial  time.Duration
	RetryFactor   float64
	RetryMax      time.Duration
	RetryAttempts int
}

// New creates a new Client with the given base URL, API key, and shem name.
func New(baseURL, apiKey, name string) *Client {
	return &Client{
		baseURL:       baseURL,
		apiKey:        apiKey,
		name:          name,
		http:          &http.Client{Timeout: 30 * time.Second},
		RetryInitial:  time.Second,
		RetryFactor:   2.0,
		RetryMax:      60 * time.Second,
		RetryAttempts: 10,
	}
}

// do performs an HTTP request with retry logic on 5xx/network errors.
func (c *Client) do(method, path string, body any) (*http.Response, error) {
	delay := c.RetryInitial
	var lastErr error

	for attempt := 0; attempt <= c.RetryAttempts; attempt++ {
		var bodyReader io.Reader
		if body != nil {
			data, err := json.Marshal(body)
			if err != nil {
				return nil, err
			}
			bodyReader = bytes.NewReader(data)
		}

		req, err := http.NewRequest(method, c.baseURL+path, bodyReader)
		if err != nil {
			return nil, err
		}

		req.Header.Set("Authorization", "Bearer "+c.apiKey)
		req.Header.Set("X-Shem-Name", c.name)
		req.Header.Set("Content-Type", "application/json")

		resp, err := c.http.Do(req)
		if err == nil && resp.StatusCode < 500 {
			return resp, nil
		}
		if resp != nil {
			resp.Body.Close()
		}

		lastErr = err
		if attempt < c.RetryAttempts {
			time.Sleep(delay)
			delay = time.Duration(float64(delay) * c.RetryFactor)
			if delay > c.RetryMax {
				delay = c.RetryMax
			}
		}
	}

	if lastErr != nil {
		return nil, fmt.Errorf("exhausted retries for %s %s: %w", method, path, lastErr)
	}
	return nil, fmt.Errorf("exhausted retries for %s %s", method, path)
}

// Register registers this shem with the orchestrator.
func (c *Client) Register(name string, repos []string) (uint, error) {
	body := map[string]any{
		"name":  name,
		"repos": repos,
	}
	resp, err := c.do("POST", "/api/shems/register", body)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	var result map[string]uint
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return 0, err
	}

	id, ok := result["shem_id"]
	if !ok {
		return 0, errors.New("missing shem_id in response")
	}
	return id, nil
}

// Deregister deregisters this shem from the orchestrator.
func (c *Client) Deregister() error {
	resp, err := c.do("DELETE", "/api/shems/me", nil)
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

// GetResumable returns tickets assigned to this shem that have a checkpoint
// and were mid-execution when the shem last died.
func (c *Client) GetResumable() ([]*ClaimResponse, error) {
	resp, err := c.do("GET", "/api/tickets/resumable", nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}
	var results []*ClaimResponse
	if err := json.NewDecoder(resp.Body).Decode(&results); err != nil {
		return nil, err
	}
	return results, nil
}

// ClaimTicket claims a ticket for this shem.
func (c *Client) ClaimTicket(id string) (*ClaimResponse, error) {
	resp, err := c.do("POST", fmt.Sprintf("/api/tickets/%s/claim", id), nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusConflict {
		return nil, ErrNotAvailable
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}

	var result ClaimResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}
	return &result, nil
}

// ClaimRevision resumes work on a ticket already assigned to this shem
// that is in the revising phase (triggered by a ticket_revise push).
// A 409 (already picked up, or requeued in the meantime) maps to
// ErrNotAvailable, same log-and-skip semantics as ClaimTicket.
func (c *Client) ClaimRevision(id string) (*ClaimResponse, error) {
	resp, err := c.do("POST", fmt.Sprintf("/api/tickets/%s/revise-claim", id), nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusConflict {
		return nil, ErrNotAvailable
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}

	var result ClaimResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}
	return &result, nil
}

// PostPhase updates the phase of a ticket.
// Returns ErrNotOwner if the orchestrator rejects the update because this shem
// no longer owns the ticket (409 Conflict — e.g. after a requeue).
func (c *Client) PostPhase(ticketID string, phase string) error {
	body := map[string]string{"phase": phase}
	resp, err := c.do("PATCH", fmt.Sprintf("/api/tickets/%s/phase", ticketID), body)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode == http.StatusConflict {
		return ErrNotOwner
	}
	return nil
}

// PostCheckpoint records a checkpoint for a ticket.
func (c *Client) PostCheckpoint(ticketID string, phase, sha string) error {
	body := map[string]string{
		"checkpoint_phase": phase,
		"checkpoint_sha":   sha,
	}
	resp, err := c.do("PATCH", fmt.Sprintf("/api/tickets/%s/checkpoint", ticketID), body)
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

// PostLog logs a message to a ticket.
func (c *Client) PostLog(ticketID string, p LogPayload) (uint, error) {
	resp, err := c.do("POST", fmt.Sprintf("/api/tickets/%s/log", ticketID), p)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	var result map[string]uint
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return 0, err
	}

	seqNum, ok := result["sequence_num"]
	if !ok {
		return 0, errors.New("missing sequence_num in response")
	}
	return seqNum, nil
}

// PostDocumentFile streams a file to the orchestrator as a document log entry.
// The file is sent as a raw body (no JSON wrapping); metadata travels in headers.
func (c *Client) PostDocumentFile(ticketID string, entryType, fromRole, filePath string) (uint, error) {
	f, err := os.Open(filePath)
	if err != nil {
		return 0, err
	}
	defer f.Close()

	url := fmt.Sprintf("%s/api/tickets/%s/log/document", c.baseURL, ticketID)
	req, err := http.NewRequest("POST", url, f)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("X-Shem-Name", c.name)
	req.Header.Set("Content-Type", "text/plain; charset=utf-8")
	req.Header.Set("X-Entry-Type", entryType)
	req.Header.Set("X-From-Role", fromRole)

	resp, err := c.http.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(resp.Body)
		return 0, fmt.Errorf("document upload failed (%d): %s", resp.StatusCode, body)
	}

	var result map[string]uint
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return 0, err
	}
	seqNum, ok := result["sequence_num"]
	if !ok {
		return 0, errors.New("missing sequence_num in response")
	}
	return seqNum, nil
}

// GetPendingInput retrieves the oldest unresolved human input for a ticket.
// Returns nil, nil if none exists.
func (c *Client) GetPendingInput(ticketID string) (*PendingInput, error) {
	return c.firstHumanInput(ticketID, "", false)
}

// PostApprovalRequest posts an approval request for a ticket.
func (c *Client) PostApprovalRequest(ticketID string, prompt string) error {
	body := map[string]any{"kind": "approval", "prompt": prompt}
	resp, err := c.do("POST", fmt.Sprintf("/api/tickets/%s/human-inputs", ticketID), body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}
	return nil
}

// GetPendingApproval retrieves the oldest pending approval for a ticket.
// Returns nil, nil if no pending approval exists.
func (c *Client) GetPendingApproval(ticketID string) (*PendingInput, error) {
	return c.firstHumanInput(ticketID, "approval", false)
}

// GetPendingFeedback retrieves the oldest unresolved feedback for a ticket.
// Returns nil, nil if none exists.
func (c *Client) GetPendingFeedback(ticketID string) (*PendingInput, error) {
	return c.firstHumanInput(ticketID, "feedback", false)
}

// AckInput resolves a human input with an empty response.
func (c *Client) AckInput(ticketID string, inputID uint) error {
	body := map[string]string{"response": "acknowledged"}
	resp, err := c.do("PATCH", fmt.Sprintf("/api/tickets/%s/human-inputs/%d", ticketID, inputID), body)
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

// firstHumanInput queries GET /human-inputs with optional kind and resolved=false filter,
// returning the first result or nil if the list is empty.
func (c *Client) firstHumanInput(ticketID string, kind string, includeResolved bool) (*PendingInput, error) {
	path := fmt.Sprintf("/api/tickets/%s/human-inputs", ticketID)
	sep := "?"
	if kind != "" {
		path += sep + "kind=" + kind
		sep = "&"
	}
	if !includeResolved {
		path += sep + "resolved=false"
	}

	resp, err := c.do("GET", path, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}

	var inputs []PendingInput
	if err := json.NewDecoder(resp.Body).Decode(&inputs); err != nil {
		return nil, err
	}
	if len(inputs) == 0 {
		return nil, nil
	}
	return &inputs[0], nil
}

// GetAvailable retrieves the first available ticket for a repo.
func (c *Client) GetAvailable(repo string) (*string, error) {
	path := "/api/tickets/available"
	if repo != "" {
		path += "?repo=" + repo
	}

	resp, err := c.do("GET", path, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var tickets []map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&tickets); err != nil {
		return nil, err
	}

	if len(tickets) == 0 {
		return nil, nil
	}

	idVal, ok := tickets[0]["id"]
	if !ok {
		return nil, errors.New("missing id in ticket")
	}

	id, ok := idVal.(string)
	if !ok {
		return nil, fmt.Errorf("unexpected ID type: %T", idVal)
	}

	return &id, nil
}
