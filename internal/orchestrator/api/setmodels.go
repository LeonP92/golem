package api

import (
	"encoding/json"
	"fmt"
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
	// A reaped ticket's shem still runs the old models; ending the record
	// stops it reclaiming, so the next claim runs these.
	result := h.DB.Model(&db.Ticket{}).
		Where("id = ? AND assigned_shem IS NULL AND phase != 'closed'", id).
		Updates(map[string]any{"models": string(selJSON), "model_backend": bound,
			"reaped_from_shem": nil, "reaped_from_phase": ""})
	if result.Error != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if result.RowsAffected == 0 {
		var n int64
		h.DB.Model(&db.Ticket{}).Where("id = ?", id).Count(&n)
		if n == 0 {
			http.Error(w, "ticket not found", http.StatusNotFound)
			return
		}
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
// raw returns a nil map, leaving the stored selections alone. A non-nil
// pinned validates against that shem's own catalog only: the one that will
// run the ticket.
func (h *Handlers) selectionColumns(w http.ResponseWriter, t db.Ticket, raw map[string]string, backend string, merge bool, pinned *db.Shem) (map[string]any, bool) {
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
			if v != "" {
				sel[k] = v
			}
		}
	}
	if backend == "" {
		backend = t.ModelBackend
	}
	if pinned != nil {
		if backend != "" && backend != pinned.Backend {
			http.Error(w, fmt.Sprintf("ticket runs on shem %s (%s), not %s", pinned.Name, pinned.Backend, backend), http.StatusBadRequest)
			return nil, false
		}
		// Only the submitted values must suit this shem: stored ones it lacks
		// are already dropped when it resolves the ticket.
		catalogs := map[string][]models.Catalog{pinned.Backend: {pinned.ModelCatalog()}}
		_, bound, err := models.ValidateSelections(catalogs, pinned.Backend, raw)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return nil, false
		}
		if bound == "" {
			bound = t.ModelBackend
		}
		selJSON, _ := json.Marshal(sel)
		return map[string]any{"models": string(selJSON), "model_backend": bound}, true
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
