package api

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/leonp92/golem/internal/orchestrator/auth"
	"github.com/leonp92/golem/internal/orchestrator/db"
	"github.com/leonp92/golem/internal/orchestrator/sse"
	ws "github.com/leonp92/golem/internal/orchestrator/ws"
)

// RegisterHumanRoutes adds human-input management and ticket action routes to mux.
func (h *Handlers) RegisterHumanRoutes(mux *http.ServeMux) {
	// Shem-facing: CRUD for human inputs (API key auth).
	mux.Handle("POST /api/tickets/{id}/human-inputs",
		auth.RequireAPIKey(h.DB)(http.HandlerFunc(h.createHumanInput)))
	mux.Handle("GET /api/tickets/{id}/human-inputs",
		auth.RequireAPIKey(h.DB)(http.HandlerFunc(h.listHumanInputs)))
	mux.Handle("PATCH /api/tickets/{id}/human-inputs/{inputID}",
		auth.RequireAPIKey(h.DB)(http.HandlerFunc(h.resolveHumanInput)))

	// Human-facing: single action dispatcher (session auth).
	mux.Handle("POST /api/tickets/{id}/actions",
		auth.RequireSession(h.DB)(http.HandlerFunc(h.ticketAction)))
}

// createHumanInput creates a new HumanInput for a ticket.
// Body: {"kind": "approval"|"feedback"|"question_answer"|"blocker_ack", "prompt": "..."}
func (h *Handlers) createHumanInput(w http.ResponseWriter, r *http.Request) {
	id, err := parseIDFromPath(r)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}

	var body struct {
		Kind   string `json:"kind"`
		Prompt string `json:"prompt"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Kind == "" || body.Prompt == "" {
		http.Error(w, "kind and prompt are required", http.StatusBadRequest)
		return
	}

	hi := db.HumanInput{
		TicketID:  id,
		Kind:      body.Kind,
		Prompt:    body.Prompt,
		CreatedAt: time.Now(),
	}
	if err := h.DB.Create(&hi).Error; err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(hi) //nolint:errcheck
}

// listHumanInputs returns human inputs for a ticket.
// Query params:
//   - kind=approval|feedback|question_answer|blocker_ack  (optional filter)
//   - resolved=false  (optional; "false" returns only unresolved)
func (h *Handlers) listHumanInputs(w http.ResponseWriter, r *http.Request) {
	id, err := parseIDFromPath(r)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}

	query := h.DB.Where("ticket_id = ?", id)
	if kind := r.URL.Query().Get("kind"); kind != "" {
		query = query.Where("kind = ?", kind)
	}
	if r.URL.Query().Get("resolved") == "false" {
		query = query.Where("resolved_at IS NULL")
	}

	var inputs []db.HumanInput
	query.Order("created_at asc").Find(&inputs)
	if inputs == nil {
		inputs = []db.HumanInput{}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(inputs) //nolint:errcheck
}

// resolveHumanInput resolves a HumanInput by recording its response.
// Body: {"response": "..."}
func (h *Handlers) resolveHumanInput(w http.ResponseWriter, r *http.Request) {
	id, err := parseIDFromPath(r)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	rawInputID := r.PathValue("inputID")
	inputID, err := strconv.ParseInt(rawInputID, 10, 64)
	if err != nil || inputID <= 0 {
		http.Error(w, "invalid inputID", http.StatusBadRequest)
		return
	}

	var body struct {
		Response string `json:"response"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	now := time.Now()
	result := h.DB.Model(&db.HumanInput{}).
		Where("id = ? AND ticket_id = ? AND resolved_at IS NULL", inputID, id).
		Updates(map[string]any{
			"response":    body.Response,
			"resolved_at": now,
		})
	if result.Error != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if result.RowsAffected == 0 {
		http.Error(w, "not found or already resolved", http.StatusNotFound)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ticketAction is the single dispatcher for all human-initiated ticket actions.
// Body: {"action": "approve"|"requeue"|"close"|"needs-attention"|"request-changes"|"answer",
//
//	"feedback": "...",   (requeue / request-changes)
//	"input_id": 123,     (answer)
//	"response": "..."}   (answer)
//
// Also accepts application/x-www-form-urlencoded for browser form submissions.
func (h *Handlers) ticketAction(w http.ResponseWriter, r *http.Request) {
	id, err := parseIDFromPath(r)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}

	var body struct {
		Action   string `json:"action"`
		Feedback string `json:"feedback"`
		InputID  uint   `json:"input_id"`
		Response string `json:"response"`
	}

	ct := r.Header.Get("Content-Type")
	if strings.HasPrefix(ct, "application/x-www-form-urlencoded") || strings.HasPrefix(ct, "multipart/form-data") {
		if err := r.ParseForm(); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		body.Action = r.FormValue("action")
		body.Feedback = r.FormValue("feedback")
		body.Response = r.FormValue("response")
		if idStr := r.FormValue("input_id"); idStr != "" {
			id64, _ := strconv.ParseUint(idStr, 10, 64)
			body.InputID = uint(id64)
		}
	} else {
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Action == "" {
			http.Error(w, "action is required", http.StatusBadRequest)
			return
		}
	}

	if body.Action == "" {
		http.Error(w, "action is required", http.StatusBadRequest)
		return
	}

	switch body.Action {
	case "approve":
		h.actionApprove(w, r, id)
	case "requeue":
		h.actionRequeue(w, r, id, body.Feedback)
	case "close":
		h.actionClose(w, r, id)
	case "needs-attention":
		h.actionNeedsAttention(w, r, id)
	case "request-changes":
		if body.Feedback == "" {
			http.Error(w, "feedback is required for request-changes", http.StatusBadRequest)
			return
		}
		h.actionRequestChanges(w, r, id, body.Feedback)
	case "answer":
		if body.InputID == 0 || body.Response == "" {
			http.Error(w, "input_id and response are required for answer", http.StatusBadRequest)
			return
		}
		h.actionAnswer(w, r, id, body.InputID, body.Response)
	default:
		http.Error(w, "unknown action: "+body.Action, http.StatusBadRequest)
	}
}

func (h *Handlers) actionApprove(w http.ResponseWriter, r *http.Request, id string) {
	var ticket db.Ticket
	if err := h.DB.First(&ticket, "id = ?", id).Error; err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}

	nextPhase, ok := nextApprovalPhase(ticket.Phase)
	if !ok {
		http.Error(w, "ticket phase does not accept approval", http.StatusBadRequest)
		return
	}

	var hi db.HumanInput
	if err := h.DB.
		Where("ticket_id = ? AND kind = 'approval' AND resolved_at IS NULL", id).
		Order("created_at asc").
		First(&hi).Error; err != nil {
		http.Error(w, "no pending approval found", http.StatusNotFound)
		return
	}

	now := time.Now()
	result := h.DB.Model(&db.HumanInput{}).
		Where("id = ? AND resolved_at IS NULL", hi.ID).
		Updates(map[string]any{"response": "approved", "resolved_at": now})
	if result.Error != nil || result.RowsAffected == 0 {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	if err := h.DB.Model(&db.Ticket{}).Where("id = ?", id).Update("phase", nextPhase).Error; err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	if err := h.appendLog(id, "ANSWER", "human", "", "Approved: "+hi.Prompt); err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	if ticket.AssignedShem != nil {
		h.Hub.Push(*ticket.AssignedShem, ws.WSMessage{ //nolint:errcheck
			Type:     "human_input_resolved",
			TicketID: strPtr(id),
			InputID:  &hi.ID,
			Response: "approved",
		})
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handlers) actionRequeue(w http.ResponseWriter, r *http.Request, id string, feedback string) {
	result := h.DB.Model(&db.Ticket{}).
		Where("id = ? AND phase NOT IN ('unassigned', 'closed')", id).
		Updates(map[string]any{
			"phase":            "unassigned",
			"assigned_shem":    nil,
			"checkpoint_phase": nil,
			"checkpoint_sha":   nil,
		})
	if result.Error != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if result.RowsAffected == 0 {
		http.Error(w, "ticket already unassigned or closed", http.StatusConflict)
		return
	}

	h.DB.Where("ticket_id = ? AND resolved_at IS NULL", id).Delete(&db.HumanInput{})

	if feedback != "" {
		if err := h.appendLog(id, "HUMAN_FEEDBACK", "human", "", feedback); err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
	}

	var ticket db.Ticket
	if h.DB.First(&ticket, "id = ?", id).Error == nil {
		h.Hub.Broadcast(ticket.RepoRemote, ws.WSMessage{
			Type:     "ticket_available",
			TicketID: strPtr(id),
			Repo:     ticket.RepoRemote,
		})
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handlers) actionClose(w http.ResponseWriter, r *http.Request, id string) {
	var ticket db.Ticket
	if err := h.DB.First(&ticket, "id = ?", id).Error; err != nil {
		http.Error(w, "ticket not found", http.StatusNotFound)
		return
	}
	result := h.DB.Model(&db.Ticket{}).
		Where("id = ? AND phase != 'closed'", id).
		Update("phase", "closed")
	if result.Error != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if result.RowsAffected == 0 {
		http.Error(w, "ticket already closed", http.StatusConflict)
		return
	}
	if ticket.AssignedShem != nil {
		h.Hub.Push(*ticket.AssignedShem, ws.WSMessage{ //nolint:errcheck
			Type:     "ticket_closed",
			TicketID: &id,
			Repo:     ticket.RepoRemote,
		})
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handlers) actionNeedsAttention(w http.ResponseWriter, r *http.Request, id string) {
	result := h.DB.Model(&db.Ticket{}).Where("id = ?", id).Update("phase", "needs-attention")
	if result.Error != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if result.RowsAffected == 0 {
		http.Error(w, "ticket not found", http.StatusNotFound)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handlers) actionRequestChanges(w http.ResponseWriter, r *http.Request, id string, feedback string) {
	var ticket db.Ticket
	if err := h.DB.First(&ticket, "id = ?", id).Error; err != nil {
		http.Error(w, "ticket not found", http.StatusNotFound)
		return
	}
	if ticket.Phase == "ready-for-review" {
		h.requestChangesFromReview(w, r, ticket, feedback)
		return
	}

	var hi db.HumanInput
	if err := h.DB.
		Where("ticket_id = ? AND kind = 'approval' AND resolved_at IS NULL", id).
		Order("created_at asc").
		First(&hi).Error; err != nil {
		http.Error(w, "no pending approval found", http.StatusNotFound)
		return
	}
	now := time.Now()
	result := h.DB.Model(&db.HumanInput{}).
		Where("id = ? AND resolved_at IS NULL", hi.ID).
		Updates(map[string]any{"response": "changes_requested", "resolved_at": now})
	if result.Error != nil || result.RowsAffected == 0 {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	fb := db.HumanInput{
		TicketID:  id,
		Kind:      "feedback",
		Prompt:    feedback,
		CreatedAt: now,
	}
	if err := h.DB.Create(&fb).Error; err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	if err := h.appendLog(id, "HUMAN_FEEDBACK", "human", "developer", feedback); err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// requestChangesFromReview handles request-changes submitted while a ticket
// is in ready-for-review: it moves the ticket to revising and wakes the
// assigned shem, instead of resolving a (nonexistent) pending approval.
func (h *Handlers) requestChangesFromReview(w http.ResponseWriter, r *http.Request, ticket db.Ticket, feedback string) {
	if ticket.AssignedShem == nil {
		http.Error(w, "ticket has no assigned shem; use requeue instead", http.StatusConflict)
		return
	}
	result := h.DB.Model(&db.Ticket{}).
		Where("id = ? AND phase = 'ready-for-review'", ticket.ID).
		Update("phase", "revising")
	if result.Error != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if result.RowsAffected == 0 {
		http.Error(w, "ticket not in ready-for-review phase", http.StatusConflict)
		return
	}

	fb := db.HumanInput{
		TicketID:  ticket.ID,
		Kind:      "feedback",
		Prompt:    feedback,
		CreatedAt: time.Now(),
	}
	if err := h.DB.Create(&fb).Error; err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	if err := h.appendLog(ticket.ID, "HUMAN_FEEDBACK", "human", "developer", feedback); err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	h.Hub.Push(*ticket.AssignedShem, ws.WSMessage{ //nolint:errcheck
		Type:     "ticket_revise",
		TicketID: strPtr(ticket.ID),
		Repo:     ticket.RepoRemote,
	})
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handlers) actionAnswer(w http.ResponseWriter, r *http.Request, id string, inputID uint, response string) {
	var hi db.HumanInput
	if err := h.DB.
		Where("id = ? AND ticket_id = ? AND resolved_at IS NULL", inputID, id).
		First(&hi).Error; err != nil {
		http.Error(w, "not found or already resolved", http.StatusNotFound)
		return
	}

	var originEntry db.LogEntry
	toRole := ""
	if h.DB.
		Where("ticket_id = ? AND message = ? AND to_role = 'human'", id, hi.Prompt).
		Order("sequence_num desc").
		First(&originEntry).Error == nil {
		toRole = originEntry.FromRole
	}

	now := time.Now()
	result := h.DB.Model(&db.HumanInput{}).
		Where("id = ? AND resolved_at IS NULL", hi.ID).
		Updates(map[string]any{"response": response, "resolved_at": now})
	if result.Error != nil || result.RowsAffected == 0 {
		http.Error(w, "already resolved", http.StatusConflict)
		return
	}

	if err := h.appendLog(id, "ANSWER", "human", toRole, response); err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	if h.Broker != nil {
		h.Broker.Publish(id, sse.LogEntryEvent{
			EntryType: "ANSWER",
			FromRole:  "human",
			ToRole:    toRole,
			Message:   response,
		})
	}

	var ticket db.Ticket
	if h.DB.First(&ticket, "id = ?", id).Error == nil && ticket.AssignedShem != nil {
		h.Hub.Push(*ticket.AssignedShem, ws.WSMessage{ //nolint:errcheck
			Type:     "human_input_resolved",
			TicketID: strPtr(id),
			InputID:  &hi.ID,
			Response: response,
		})
	}
	w.WriteHeader(http.StatusNoContent)
}

// nextApprovalPhase returns the next phase for an approval transition.
func nextApprovalPhase(current string) (string, bool) {
	switch current {
	case "brainstorm":
		return "plan", true
	case "plan":
		return "implement", true
	}
	return "", false
}

// appendLog inserts a LogEntry for the given ticket, assigning the next sequence_num.
func (h *Handlers) appendLog(ticketID string, entryType, fromRole, toRole, message string) error {
	var maxSeq struct{ Max *uint }
	h.DB.Model(&db.LogEntry{}).
		Select("MAX(sequence_num) as max").
		Where("ticket_id = ?", ticketID).
		Scan(&maxSeq)
	var nextSeq uint = 1
	if maxSeq.Max != nil {
		nextSeq = *maxSeq.Max + 1
	}
	entry := db.LogEntry{
		TicketID:    ticketID,
		SequenceNum: nextSeq,
		EntryType:   entryType,
		FromRole:    fromRole,
		ToRole:      toRole,
		Message:     message,
		CreatedAt:   time.Now(),
	}
	return h.DB.Create(&entry).Error
}
