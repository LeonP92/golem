package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/leonp92/golem/internal/orchestrator/auth"
	"github.com/leonp92/golem/internal/orchestrator/db"
	"github.com/leonp92/golem/internal/orchestrator/ghsync"
	"gorm.io/gorm"
)

// SyncTrigger requests an immediate ingest pass for one repository. It is
// satisfied by *ghsync.Worker; the interface keeps this package free of a
// dependency on the worker's internals and lets tests substitute a stub.
type SyncTrigger interface {
	TriggerSync(repoID uint) bool
}

// defaultManualSyncCooldown is used only as a defensive fallback when
// Handlers.ManualSyncCooldown is left unset (its zero value) — for example by
// a wiring bug. In normal operation the cooldown comes from
// config.GitHubConfig.ManualSyncCooldownDuration, which already guarantees a
// positive value; this fallback exists so a missing cooldown never silently
// degrades into "no cooldown at all", which would defeat the point of this
// endpoint (a held-down button could exhaust the hourly GitHub rate limit).
const defaultManualSyncCooldown = time.Minute

// RegisterGitHubRoutes adds GitHub integration routes to mux. Manual sync is
// session-authenticated only: it is a deliberate human action, and a shem
// holding a valid API key must not be able to drive GitHub polling.
func (h *Handlers) RegisterGitHubRoutes(mux *http.ServeMux) {
	mux.Handle("POST /api/github/repos/{id}/sync",
		auth.RequireSession(h.DB)(auth.RequireCSRF(http.HandlerFunc(h.manualSync))))
	mux.Handle("POST /api/tickets/{id}/branch-pushed",
		auth.RequireAPIKey(h.DB)(http.HandlerFunc(h.branchPushed)))
}

// manualSync triggers an out-of-band ingest pass for one repo, subject to a
// per-repo cooldown.
//
// The repo is resolved from the database and its Enabled flag checked BEFORE
// h.Sync.TriggerSync is ever called, and in that order. Worker.triggers (see
// ghsync.Worker) is an unbounded map keyed by the repo ID passed to
// TriggerSync, with entries that are never deleted — calling it with an
// unvalidated path parameter would let any authenticated caller grow that map
// without bound by hitting this endpoint with arbitrary IDs. Do not reorder
// the 404/409 checks below to after the TriggerSync call.
//
// LastManualSync is stamped only once TriggerSync has actually been called —
// a 404, 409, 503, or 429 response must never extend the cooldown window.
//
// Every refusal answers JSON, not text/plain. The only caller is the Sync now
// button, a hand-written fetch() that (correctly) refuses to parse a non-JSON
// body — auth.RequireSession 302-redirects an expired session to an HTML
// login page and fetch follows it. That guard meant a text/plain 404, 409 or
// 503 landed in the same catch as the login page and rendered the same
// "Failed — retry?", so three distinct, separately actionable failures were
// indistinguishable. The status codes are the contract and are unchanged;
// only the body is, and each message says what the operator can do next.
func (h *Handlers) manualSync(w http.ResponseWriter, r *http.Request) {
	id64, err := strconv.ParseUint(r.PathValue("id"), 10, 64)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid repository id")
		return
	}
	repoID := uint(id64)

	var repo db.GitHubRepo
	if err := h.DB.First(&repo, repoID).Error; err != nil {
		writeJSONError(w, http.StatusNotFound,
			"repository not found — reload this page, it may have been removed since it was rendered")
		return
	}
	if !repo.Enabled {
		writeJSONError(w, http.StatusConflict,
			"sync is not enabled for this repository — tick Enabled above first")
		return
	}

	// After the cold-start fix in cmd/orchestrator (githubSyncPlan), the
	// worker is built whenever a token is present, so the remaining causes of
	// a nil Sync are an unset token or a client that failed to initialise —
	// both fixed at startup, neither fixable from this page. Say so.
	if h.Sync == nil {
		writeJSONError(w, http.StatusServiceUnavailable,
			"GitHub sync is not running on this orchestrator — GOLEM_GITHUB_TOKEN "+
				"was unset or invalid at startup; set it and restart")
		return
	}

	cooldown := h.ManualSyncCooldown
	if cooldown <= 0 {
		cooldown = defaultManualSyncCooldown
	}
	if repo.LastManualSync != nil {
		if remaining := cooldown - time.Since(*repo.LastManualSync); remaining > 0 {
			retryAfter := int(remaining.Seconds()) + 1
			w.Header().Set("Retry-After", strconv.Itoa(retryAfter))
			writeJSON(w, http.StatusTooManyRequests, map[string]any{
				"error":            "manual sync is cooling down",
				"retry_after_secs": retryAfter,
			})
			return
		}
	}

	queued := h.Sync.TriggerSync(repoID)

	if err := h.DB.Model(&repo).Updates(map[string]any{"last_manual_sync": time.Now()}).Error; err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal error")
		return
	}

	writeJSON(w, http.StatusAccepted, map[string]any{
		// queued=false means a sync was already pending for this repo — same
		// effect as this request's own trigger would have had.
		"queued": queued,
	})
}

// enqueuePRIfReady queues the pull-request write when both preconditions hold:
// the ticket has reached ready-for-review, and its branch exists on the
// remote. Whichever of the two events happens second is the one that enqueues.
// Both call sites use ghsync.PRKey, so a race between them yields exactly one
// pull request.
func enqueuePRIfReady(tx *gorm.DB, ticket db.Ticket) error {
	if ticket.IssueNumber == nil || !ticket.BranchPushed || ticket.Phase != "ready-for-review" {
		return nil
	}
	base := ticket.BaseBranch
	if base == "" {
		base = "main"
	}
	payload, err := json.Marshal(ghsync.PRPayload{
		Head:  ticket.Branch,
		Base:  base,
		Title: ticket.Title,
		Body:  fmt.Sprintf("Closes #%d\n\nOpened by Golem.", *ticket.IssueNumber),
	})
	if err != nil {
		return fmt.Errorf("marshal pr payload: %w", err)
	}
	if err := ghsync.Enqueue(tx, db.GitHubOutbox{
		TicketID:       ticket.ID,
		Kind:           ghsync.KindPR,
		Payload:        string(payload),
		IdempotencyKey: ghsync.PRKey(ticket.ID),
	}); err != nil {
		return fmt.Errorf("enqueue pr: %w", err)
	}
	return nil
}

// branchPushed records that the shem published the ticket branch, and queues
// the pull request if the ticket is already at ready-for-review.
func (h *Handlers) branchPushed(w http.ResponseWriter, r *http.Request) {
	id, err := parseIDFromPath(r)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	shem := auth.ShemFromRequest(r)

	txErr := h.DB.Transaction(func(tx *gorm.DB) error {
		result := tx.Model(&db.Ticket{}).
			Where("id = ? AND assigned_shem = ?", id, shem.ID).
			Update("branch_pushed", true)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return errNotOwner
		}
		var ticket db.Ticket
		if err := tx.First(&ticket, "id = ?", id).Error; err != nil {
			return fmt.Errorf("reload ticket %s: %w", id, err)
		}
		return enqueuePRIfReady(tx, ticket)
	})
	if errors.Is(txErr, errNotOwner) {
		http.Error(w, "ticket not owned by this shem", http.StatusConflict)
		return
	}
	if txErr != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(body) //nolint:errcheck
}

// writeJSONError answers a refusal in the one shape a fetch() caller can
// read. The "error" key is the same one the 429 cooldown response already
// uses, so the page has a single place to look for something to show.
func writeJSONError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]any{"error": msg})
}
