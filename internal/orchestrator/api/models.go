package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/leonp92/golem/internal/models"
	"github.com/leonp92/golem/internal/orchestrator/auth"
	"github.com/leonp92/golem/internal/orchestrator/db"
	"github.com/leonp92/golem/internal/orchestrator/rbac"
)

// RegisterModelRoutes adds the model-catalog route to mux.
func (h *Handlers) RegisterModelRoutes(mux *http.ServeMux) {
	mux.Handle("GET /api/models",
		auth.RequireSession(h.DB)(rbac.Require(rbac.PermTicketCreate)(http.HandlerFunc(h.listModels))))
}

// listModels returns each backend's union catalog and the stage list.
func (h *Handlers) listModels(w http.ResponseWriter, r *http.Request) {
	fleet, err := db.LoadFleet(h.DB)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	out := struct {
		Stages   []models.Stage      `json:"stages"`
		Backends []db.BackendCatalog `json:"backends"`
	}{models.Stages, fleet.Backends()}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(out) //nolint:errcheck
}

// resolveForShem resolves a ticket's selections against one shem's catalog.
// It logs a WARNING for anything it had to drop, once per distinct message.
func (h *Handlers) resolveForShem(t db.Ticket, shem db.Shem) (string, map[string]string) {
	sel := t.ModelSelections()
	if t.ModelBackend != "" && t.ModelBackend != shem.Backend {
		// The ids belong to another vendor, so none of them can be used.
		h.warnOnce(t.ID, fmt.Sprintf(
			"ticket is bound to backend %s but shem %s runs %s; using vendor defaults",
			t.ModelBackend, shem.Name, shem.Backend))
		sel = models.Selections{}
	}
	resolved, dropped := models.Resolve(shem.ModelCatalog(), sel)
	if len(dropped) > 0 {
		h.warnOnce(t.ID, fmt.Sprintf(
			"dropped model selection %s: not in shem %s's catalog (%s); using stage defaults",
			strings.Join(dropped, ", "), shem.Name, shem.Backend))
	}
	out := make(map[string]string, len(resolved))
	for st, id := range resolved {
		out[string(st)] = id
	}
	return shem.Backend, out
}

// claimResponse builds the response for a ticket handed to shem, with its
// models resolved against that shem's catalog.
func (h *Handlers) claimResponse(t db.Ticket, shem db.Shem) *ClaimResponse {
	var entries []db.LogEntry
	h.DB.Where("ticket_id = ?", t.ID).Order("sequence_num asc").Find(&entries)
	if entries == nil {
		entries = []db.LogEntry{}
	}
	backend, resolved := h.resolveForShem(t, shem)
	return &ClaimResponse{
		TicketID:        t.ID,
		Branch:          t.Branch,
		BaseBranch:      t.BaseBranch,
		Title:           t.Title,
		RepoRemote:      t.RepoRemote,
		Description:     t.Description,
		CheckpointPhase: t.CheckpointPhase,
		CheckpointSHA:   t.CheckpointSHA,
		LogEntries:      entries,
		Backend:         backend,
		Models:          resolved,
	}
}

// shemRow loads one shem row by id. A zero row would read as "matches every
// ticket, resolves everything to the vendor default".
func (h *Handlers) shemRow(id uint) (db.Shem, error) {
	var s db.Shem
	if err := h.DB.First(&s, "id = ?", id).Error; err != nil {
		return db.Shem{}, fmt.Errorf("shem %d not found: %w", id, err)
	}
	return s, nil
}

// warnOnce appends a WARNING unless the ticket already carries the same one.
// The resumable poll resolves a running ticket every few seconds.
func (h *Handlers) warnOnce(ticketID, msg string) {
	var n int64
	h.DB.Model(&db.LogEntry{}).Where("ticket_id = ? AND entry_type = ? AND message = ?", ticketID, "WARNING", msg).Count(&n)
	if n == 0 {
		_ = h.appendLog(ticketID, "WARNING", "orchestrator", "", msg)
	}
}
