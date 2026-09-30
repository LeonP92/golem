package api

import (
	"encoding/json"
	"net/http"

	"github.com/leonp92/golem/internal/orchestrator/auth"
	"github.com/leonp92/golem/internal/orchestrator/db"
	"gorm.io/gorm"
)

// reclaimablePredicate matches a ticket the heartbeat reaper released from a
// given shem that nobody has claimed since. The approval clause is the same
// gate ClaimTicket applies: a GitHub ticket whose issue body changed after
// approval must not go back to work on the strength of the old approval.
const reclaimablePredicate = "reaped_from_shem = ? AND phase = 'unassigned' AND assigned_shem IS NULL AND " +
	"(issue_number IS NULL OR (intake_approved AND approved_body_hash <> '' AND approved_body_hash = body_hash))"

// reapedTickets lists the tickets the reaper released from the calling shem.
// The shem intersects this with what it is still running and reclaims those.
//
// It is a read so the shem can poll it cheaply: nearly always it is empty,
// and an UPDATE that matches nothing still takes SQLite's write lock — the
// contention that delays heartbeats and causes reaping in the first place.
func (h *Handlers) reapedTickets(w http.ResponseWriter, r *http.Request) {
	shem := auth.ShemFromRequest(r)
	var tickets []db.Ticket
	if err := h.DB.Where(reclaimablePredicate, shem.ID).Select("id").Find(&tickets).Error; err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	type summary struct {
		TicketID string `json:"ticket_id"`
	}
	out := make([]summary, len(tickets))
	for i, t := range tickets {
		out[i] = summary{TicketID: t.ID}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(out) //nolint:errcheck
}

// reclaimTicket gives a reaped ticket back to the shem it was reaped from,
// in the phase it was in. The shem calls this only for a ticket it is still
// running, so its agent carries on and its next phase report is accepted.
//
// No GitHub label is queued: the reaper queued none when it released the
// ticket, so the issue still shows the phase being restored here.
func (h *Handlers) reclaimTicket(w http.ResponseWriter, r *http.Request) {
	id, err := parseIDFromPath(r)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	shem := auth.ShemFromRequest(r)
	result := h.DB.Model(&db.Ticket{}).
		Where("id = ? AND "+reclaimablePredicate, id, shem.ID).
		Updates(map[string]any{
			"phase":             gorm.Expr("reaped_from_phase"),
			"assigned_shem":     shem.ID,
			"reaped_from_shem":  nil,
			"reaped_from_phase": "",
		})
	if result.Error != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if result.RowsAffected == 0 {
		http.Error(w, "ticket is not reclaimable", http.StatusConflict)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
