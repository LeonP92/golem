package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/leonp92/golem/internal/orchestrator/auth"
	"github.com/leonp92/golem/internal/orchestrator/db"
	"github.com/leonp92/golem/internal/orchestrator/urlnorm"
	ws "github.com/leonp92/golem/internal/orchestrator/ws"
)

// ticketResponse wraps a db.Ticket with a resolved creator username for
// JSON responses, so clients don't need a second lookup to render
// "created by X".
type ticketResponse struct {
	db.Ticket
	CreatedBy string `json:"created_by"`
}

func toTicketResponse(t db.Ticket, names map[uint]string) ticketResponse {
	created := ""
	if t.CreatedByUserID != nil {
		created = names[*t.CreatedByUserID]
	}
	return ticketResponse{Ticket: t, CreatedBy: created}
}

// ClaimResponse is returned by a successful ticket claim.
type ClaimResponse struct {
	TicketID        string        `json:"ticket_id"`
	Branch          string        `json:"branch"`
	RepoRemote      string        `json:"repo_remote"`
	Description     string        `json:"description"`
	CheckpointPhase *string       `json:"checkpoint_phase"`
	CheckpointSHA   *string       `json:"checkpoint_sha"`
	LogEntries      []db.LogEntry `json:"log_entries"`
}

// ClaimTicket atomically claims a ticket for shemID. Exactly one concurrent
// caller wins; all others receive a non-nil error.
func (h *Handlers) ClaimTicket(ticketID string, shemID uint) (*ClaimResponse, error) {
	result := h.DB.Model(&db.Ticket{}).
		Where("id = ? AND phase = 'unassigned'", ticketID).
		Updates(map[string]any{"phase": "claimed", "assigned_shem": shemID})
	if result.Error != nil {
		return nil, result.Error
	}
	if result.RowsAffected == 0 {
		return nil, fmt.Errorf("ticket not available")
	}
	var ticket db.Ticket
	h.DB.First(&ticket, "id = ?", ticketID)
	var entries []db.LogEntry
	h.DB.Where("ticket_id = ?", ticketID).Order("sequence_num asc").Find(&entries)
	if entries == nil {
		entries = []db.LogEntry{}
	}
	return &ClaimResponse{
		TicketID:        ticketID,
		Branch:          ticket.Branch,
		RepoRemote:      ticket.RepoRemote,
		Description:     ticket.Description,
		CheckpointPhase: ticket.CheckpointPhase,
		CheckpointSHA:   ticket.CheckpointSHA,
		LogEntries:      entries,
	}, nil
}

// RegisterTicketRoutes adds ticket-related routes to mux.
func (h *Handlers) RegisterTicketRoutes(mux *http.ServeMux) {
	mux.Handle("POST /api/tickets", auth.RequireSession(h.DB)(http.HandlerFunc(h.createTicket)))
	mux.Handle("GET /api/tickets", auth.RequireSession(h.DB)(http.HandlerFunc(h.listTickets)))
	mux.Handle("GET /api/tickets/available", auth.RequireAPIKey(h.DB)(http.HandlerFunc(h.availableTickets)))
	mux.Handle("GET /api/tickets/{id}", auth.RequireSession(h.DB)(http.HandlerFunc(h.getTicket)))
	mux.Handle("POST /api/tickets/{id}/claim", auth.RequireAPIKey(h.DB)(http.HandlerFunc(h.claimTicket)))
	mux.Handle("PATCH /api/tickets/{id}/phase", auth.RequireAPIKey(h.DB)(http.HandlerFunc(h.updatePhase)))
	mux.Handle("PATCH /api/tickets/{id}/checkpoint", auth.RequireAPIKey(h.DB)(http.HandlerFunc(h.updateCheckpoint)))
}

func (h *Handlers) createTicket(w http.ResponseWriter, r *http.Request) {
	var body struct {
		RepoRemote  string `json:"repo_remote"`
		Branch      string `json:"branch"`
		Description string `json:"description"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if body.RepoRemote == "" || body.Branch == "" || body.Description == "" {
		http.Error(w, "repo_remote, branch, and description are required", http.StatusBadRequest)
		return
	}
	user := auth.SessionUser(r)
	ticket := db.Ticket{
		RepoRemote:  urlnorm.Normalize(body.RepoRemote),
		Branch:      body.Branch,
		Description: body.Description,
		Phase:       "unassigned",
	}
	if user != nil {
		ticket.CreatedByUserID = &user.ID
	}
	if err := h.DB.Create(&ticket).Error; err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	names := db.CreatorNames(h.DB, []db.Ticket{ticket})
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(toTicketResponse(ticket, names)) //nolint:errcheck
}

func (h *Handlers) listTickets(w http.ResponseWriter, r *http.Request) {
	var tickets []db.Ticket
	h.DB.Order("created_at desc").Find(&tickets)
	names := db.CreatorNames(h.DB, tickets)
	resp := make([]ticketResponse, len(tickets))
	for i, t := range tickets {
		resp[i] = toTicketResponse(t, names)
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp) //nolint:errcheck
}

func (h *Handlers) getTicket(w http.ResponseWriter, r *http.Request) {
	id, err := parseIDFromPath(r)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	var ticket db.Ticket
	if err := h.DB.First(&ticket, "id = ?", id).Error; err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	names := db.CreatorNames(h.DB, []db.Ticket{ticket})
	var entries []db.LogEntry
	h.DB.Where("ticket_id = ?", id).Order("sequence_num asc").Find(&entries)
	if entries == nil {
		entries = []db.LogEntry{}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{ //nolint:errcheck
		"ticket":      toTicketResponse(ticket, names),
		"log_entries": entries,
	})
}

func (h *Handlers) availableTickets(w http.ResponseWriter, r *http.Request) {
	repo := r.URL.Query().Get("repo")
	query := h.DB.Where("phase = 'unassigned'")
	if repo != "" {
		query = query.Where("repo_remote = ?", urlnorm.Normalize(repo))
	}
	var tickets []db.Ticket
	query.Order("created_at asc").Find(&tickets)
	if tickets == nil {
		tickets = []db.Ticket{}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(tickets) //nolint:errcheck
}

func (h *Handlers) claimTicket(w http.ResponseWriter, r *http.Request) {
	id, err := parseIDFromPath(r)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	shem := auth.ShemFromRequest(r)
	resp, err := h.ClaimTicket(id, shem.ID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	h.Hub.Broadcast(resp.RepoRemote, ws.WSMessage{
		Type:     "ticket_claimed",
		TicketID: strPtr(resp.TicketID),
	})
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp) //nolint:errcheck
}

func (h *Handlers) updatePhase(w http.ResponseWriter, r *http.Request) {
	id, err := parseIDFromPath(r)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	shem := auth.ShemFromRequest(r)
	var body struct {
		Phase string `json:"phase"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Phase == "" {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	validPhases := map[string]bool{
		"brainstorm": true, "plan": true, "implement": true,
		"review": true, "ready-for-review": true, "needs-attention": true,
		"in-progress": true,
	}
	if !validPhases[body.Phase] {
		http.Error(w, "invalid phase", http.StatusBadRequest)
		return
	}
	result := h.DB.Model(&db.Ticket{}).
		Where("id = ? AND assigned_shem = ?", id, shem.ID).
		Updates(map[string]any{"phase": body.Phase})
	if result.RowsAffected == 0 {
		http.Error(w, "ticket not owned by this shem", http.StatusConflict)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handlers) updateCheckpoint(w http.ResponseWriter, r *http.Request) {
	id, err := parseIDFromPath(r)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	shem := auth.ShemFromRequest(r)
	var body struct {
		Phase string `json:"checkpoint_phase"`
		SHA   string `json:"checkpoint_sha"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	result := h.DB.Model(&db.Ticket{}).
		Where("id = ? AND assigned_shem = ?", id, shem.ID).
		Updates(map[string]any{
			"checkpoint_phase": body.Phase,
			"checkpoint_sha":   body.SHA,
		})
	if result.RowsAffected == 0 {
		http.Error(w, "not found or not owner", http.StatusNotFound)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// parseIDFromPath extracts the {id} path value from a request using Go 1.22+
// pattern matching (PathValue).
func parseIDFromPath(r *http.Request) (string, error) {
	raw := r.PathValue("id")
	if raw == "" {
		// Fallback: extract last path segment.
		parts := strings.Split(strings.TrimSuffix(r.URL.Path, "/"), "/")
		raw = parts[len(parts)-1]
	}
	if raw == "" {
		return "", fmt.Errorf("missing id")
	}
	return raw, nil
}

// strPtr returns a pointer to the given string value.
func strPtr(v string) *string { return &v }
