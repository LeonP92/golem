package api

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/leonp92/golem/internal/orchestrator/auth"
	"github.com/leonp92/golem/internal/orchestrator/db"
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
		auth.RequireSession(h.DB)(http.HandlerFunc(h.manualSync)))
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
func (h *Handlers) manualSync(w http.ResponseWriter, r *http.Request) {
	id64, err := strconv.ParseUint(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	repoID := uint(id64)

	var repo db.GitHubRepo
	if err := h.DB.First(&repo, repoID).Error; err != nil {
		http.Error(w, "repo not found", http.StatusNotFound)
		return
	}
	if !repo.Enabled {
		http.Error(w, "sync is not enabled for this repository", http.StatusConflict)
		return
	}

	if h.Sync == nil {
		http.Error(w, "github sync is not running", http.StatusServiceUnavailable)
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
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	writeJSON(w, http.StatusAccepted, map[string]any{
		// queued=false means a sync was already pending for this repo — same
		// effect as this request's own trigger would have had.
		"queued": queued,
	})
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(body) //nolint:errcheck
}
