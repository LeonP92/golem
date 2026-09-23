package api

import (
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/leonp92/golem/internal/orchestrator/db"
	ws "github.com/leonp92/golem/internal/orchestrator/ws"
	"gorm.io/gorm"
)

// PhaseStopped is a ticket a human has interrupted. Nothing automatic moves
// it: not the claim poll, not resume after a shem restart, not the
// pull-request monitor. Only a person starting it again.
const PhaseStopped = "stopped"

var (
	errNotStoppable = errors.New("ticket cannot be stopped from this phase")
	errNotStopped   = errors.New("ticket is not stopped")
)

// actionStop interrupts whatever the shem is doing and parks the ticket.
//
// The phase is recorded before it is overwritten, because "put it back where
// it was" is the only resume rule that does not require guessing: a ticket
// stopped mid-implement should not come back at brainstorm, and a rule
// derived from the checkpoint would do exactly that.
//
// The cancel reaches the shem as a websocket push, and that push is best
// effort — but the STOP is not. The phase change is what holds: every
// automatic path excludes stopped, so even a shem that never receives the
// message finds the ticket un-claimable, un-resumable and un-revisable the
// moment it next asks. A shem still mid-run finishes its current phase and
// then cannot advance, which is the safe direction to fail.
func (h *Handlers) actionStop(w http.ResponseWriter, r *http.Request, id string) {
	var ticket db.Ticket
	if err := h.DB.First(&ticket, "id = ?", id).Error; err != nil {
		http.Error(w, "ticket not found", http.StatusNotFound)
		return
	}

	txErr := h.DB.Transaction(func(tx *gorm.DB) error {
		result := tx.Model(&db.Ticket{}).
			Where("id = ? AND phase NOT IN ?", id,
				// pending-approval is excluded for a different reason from
				// the other two. There is nothing running to interrupt —
				// no shem has it — and more importantly the intake gate's
				// invariant is that approval is the ONLY way out of that
				// phase. A stop would be a second exit, and one that hides
				// the ticket from the approval queue while it waits. The
				// controls for a ticket nobody has started are approve and
				// close.
				[]string{PhaseStopped, "closed", "pending-approval"}).
			Updates(map[string]any{
				"phase":              PhaseStopped,
				"stopped_from_phase": ticket.Phase,
			})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return errNotStoppable
		}
		return appendLogTx(tx, id, "STATUS", "human", "",
			fmt.Sprintf("Stopped by a human during %s. It will not be picked up again "+
				"until someone starts it.", ticket.Phase))
	})
	if errors.Is(txErr, errNotStoppable) {
		http.Error(w, "ticket cannot be stopped from this phase", http.StatusConflict)
		return
	}
	if txErr != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	if ticket.AssignedShem != nil && h.Hub != nil {
		h.Hub.Push(*ticket.AssignedShem, ws.WSMessage{ //nolint:errcheck
			Type: "ticket_stop", TicketID: &id, Repo: ticket.RepoRemote,
		})
	}
	w.WriteHeader(http.StatusNoContent)
}

// actionResume puts a stopped ticket back into the phase it was interrupted
// in. Only valid from stopped: anywhere else this would be a way to rewrite
// a running ticket's phase from the UI.
func (h *Handlers) actionResume(w http.ResponseWriter, r *http.Request, id string) {
	var ticket db.Ticket
	if err := h.DB.First(&ticket, "id = ?", id).Error; err != nil {
		http.Error(w, "ticket not found", http.StatusNotFound)
		return
	}
	back := ticket.StoppedFromPhase
	if back == "" {
		// Stopped before this column existed, or written by something that
		// did not record it. Unassigned is the one phase that is always
		// safe to re-enter: it is where a ticket waits to be claimed.
		back = "unassigned"
	}

	txErr := h.DB.Transaction(func(tx *gorm.DB) error {
		result := tx.Model(&db.Ticket{}).
			Where("id = ? AND phase = ?", id, PhaseStopped).
			Updates(map[string]any{"phase": back, "stopped_from_phase": ""})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return errNotStopped
		}
		return appendLogTx(tx, id, "STATUS", "human", "",
			fmt.Sprintf("Started again by a human; continuing from %s.", back))
	})
	if errors.Is(txErr, errNotStopped) {
		http.Error(w, "ticket is not stopped", http.StatusConflict)
		return
	}
	if txErr != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// appendLogTx writes a log entry inside an existing transaction. The
// Handlers.appendLog method cannot be used here: it writes through h.DB, so
// the entry would land outside the transaction and survive a rollback,
// leaving the ticket claiming something that did not happen.
func appendLogTx(tx *gorm.DB, ticketID, entryType, fromRole, toRole, message string) error {
	var maxSeq struct{ Max *uint }
	tx.Model(&db.LogEntry{}).Select("MAX(sequence_num) as max").
		Where("ticket_id = ?", ticketID).Scan(&maxSeq)
	var next uint = 1
	if maxSeq.Max != nil {
		next = *maxSeq.Max + 1
	}
	return tx.Create(&db.LogEntry{
		TicketID: ticketID, SequenceNum: next, EntryType: entryType,
		FromRole: fromRole, ToRole: toRole, Message: message, CreatedAt: time.Now(),
	}).Error
}
