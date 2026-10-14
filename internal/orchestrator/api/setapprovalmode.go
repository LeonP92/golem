package api

import (
	"net/http"

	"github.com/leonp92/golem/internal/orchestrator/db"
)

// actionSetApprovalMode switches a ticket between manual approval (a human
// approves the spec and the plan) and automatic (the shem's own validation is
// the gate). Like set-models it only applies to an unclaimed, open ticket: the
// shem reads the mode once, when it claims, so changing it under a running
// shem would silently do nothing.
func (h *Handlers) actionSetApprovalMode(w http.ResponseWriter, r *http.Request, id string, manual bool) {
	result := h.DB.Model(&db.Ticket{}).
		Where("id = ? AND assigned_shem IS NULL AND phase != 'closed'", id).
		Update("manual_approval", manual)
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
	msg := "approval mode: automatic (spec and plan advance after validation)"
	if manual {
		msg = "approval mode: manual (a human approves the spec and the plan)"
	}
	if err := h.appendLog(id, "STATUS", "human", "", msg); err != nil {
		http.Error(w, "failed to log", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
