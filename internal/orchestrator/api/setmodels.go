package api

import (
	"encoding/json"
	"net/http"

	"github.com/leonp92/golem/internal/models"
	"github.com/leonp92/golem/internal/orchestrator/db"
)

// actionSetModels replaces a ticket's model selections; unclaimed tickets only.
func (h *Handlers) actionSetModels(w http.ResponseWriter, r *http.Request, id string, raw map[string]string, backend string) {
	fleet, err := db.LoadFleet(h.DB)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	sel, bound, err := models.ValidateSelections(fleet.Catalogs(), backend, raw)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	selJSON, _ := json.Marshal(sel)
	result := h.DB.Model(&db.Ticket{}).
		Where("id = ? AND assigned_shem IS NULL AND phase != 'closed'", id).
		Updates(map[string]any{"models": string(selJSON), "model_backend": bound})
	if result.RowsAffected == 0 {
		http.Error(w, "ticket is claimed or closed", http.StatusConflict)
		return
	}
	msg := "model selections cleared"
	if len(sel) > 0 {
		msg = "model selections set: " + models.Describe(sel)
	}
	if err := h.appendLog(id, "STATUS", "human", "", msg); err != nil {
		http.Error(w, "failed to log", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// selectionColumns validates raw and returns the models and model_backend
// values to write, or ok=false with the response already written. An empty
// raw returns a nil map, leaving the stored selections alone.
func (h *Handlers) selectionColumns(w http.ResponseWriter, t db.Ticket, raw map[string]string, backend string, merge bool) (map[string]any, bool) {
	if len(raw) == 0 {
		return nil, true
	}
	sel := raw
	if merge {
		sel = map[string]string{}
		for k, v := range t.ModelSelections() {
			sel[k] = v
		}
		for k, v := range raw {
			sel[k] = v
		}
	}
	if backend == "" {
		backend = t.ModelBackend
	}
	fleet, err := db.LoadFleet(h.DB)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return nil, false
	}
	out, bound, err := models.ValidateSelections(fleet.Catalogs(), backend, sel)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return nil, false
	}
	selJSON, _ := json.Marshal(out)
	return map[string]any{"models": string(selJSON), "model_backend": bound}, true
}
