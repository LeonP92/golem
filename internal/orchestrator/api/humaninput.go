package api

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/leonp92/golem/internal/orchestrator/db"
)

// This file holds the shem-facing CRUD for human inputs. The routes are
// registered in human.go alongside the human-facing action dispatcher,
// because the two halves are the two ends of the same conversation: a shem
// raises a question or an approval request here, and a human answers it
// there.

// createHumanInput creates a new HumanInput for the calling shem's ticket.
// Body: {"kind": "approval"|"feedback"|"question_answer"|"blocker_ack", "prompt": "..."}
func (h *Handlers) createHumanInput(w http.ResponseWriter, r *http.Request) {
	id, err := parseIDFromPath(r)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	if !h.writeTicketOwnership(w, r, id) {
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
	if !h.writeTicketOwnership(w, r, id) {
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
	if !h.writeTicketOwnership(w, r, id) {
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
	// kind = 'feedback' is the whole point of this clause. This endpoint
	// authenticates with a SHEM API KEY, and the shem's only legitimate use
	// of it is consumeFeedback acking the feedback it has just read.
	//
	// Without the clause the same key resolves any unresolved input on a
	// ticket the shem owns — including an approval, which is the human gate.
	// An agent that reaches the key (it runs as root in the shem container
	// and the key is in the environment) could answer the question that was
	// meant for a person.
	//
	// Still load-bearing after brainstorm and plan approvals became
	// automatic: those stages no longer create approval rows, but intake
	// does, and "the shem cannot close a human's question" should not depend
	// on which rows happen to exist today.
	result := h.DB.Model(&db.HumanInput{}).
		Where("id = ? AND ticket_id = ? AND resolved_at IS NULL AND kind = ?", inputID, id, "feedback").
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
