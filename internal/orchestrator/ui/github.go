package ui

import (
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"

	"github.com/leonp92/golem/internal/orchestrator/db"
	"github.com/leonp92/golem/internal/orchestrator/ghsync"
	"github.com/leonp92/golem/internal/orchestrator/urlnorm"
	"gorm.io/gorm"
)

// githubSettings renders per-repo GitHub sync settings. Repos are discovered
// from registered shems, so a repo appears here as soon as a shem serving it
// registers — enabling sync stays a deliberate human action. A repo a shem
// has registered but that has no db.GitHubRepo row yet is shown as a
// disabled placeholder with ID 0; see the template for how that placeholder
// is kept from offering a "Sync now" action (POST .../0/sync would 404).
func (h *Handlers) githubSettings(w http.ResponseWriter, r *http.Request) {
	h.renderGitHubSettings(w, r, "")
}

// renderGitHubSettings renders the settings page, optionally with a visible
// error banner. errMsg != "" also switches the response to 400: a rejected
// submission must not look like a successful one to anything that reads the
// status code.
func (h *Handlers) renderGitHubSettings(w http.ResponseWriter, r *http.Request, errMsg string) {
	var repos []db.GitHubRepo
	h.DB.Order("repo_remote asc").Find(&repos)

	known := make(map[string]bool, len(repos))
	for _, rp := range repos {
		known[rp.RepoRemote] = true
	}
	for _, remote := range h.shemsRepos() {
		if known[remote] {
			continue
		}
		owner, name := splitRemote(remote)
		repos = append(repos, db.GitHubRepo{
			RepoRemote: remote, Owner: owner, Name: name, Label: "golem",
		})
	}
	// Parked outbox rows are rendered here because this is the only page
	// about the GitHub integration, and because until fix round 1b they were
	// rendered nowhere at all (finding I6): a parked comment or pull-request
	// write was lost permanently behind one log line, and the parked row
	// also made every later 304 poll pay a full reconcile forever. A failure
	// to load them degrades to an empty list rather than blanking the page —
	// the repo settings above are what an operator most often comes here for.
	parked, err := ghsync.ParkedRows(h.DB)
	if err != nil {
		log.Printf("ui: load parked outbox rows: %v", err)
	}

	if errMsg != "" {
		// Content-Type must be set before WriteHeader, or render's own
		// Set() lands after the headers have already gone out.
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusBadRequest)
	}
	h.render(w, r, "github_settings", map[string]any{
		"Repos": repos, "Parked": parked, "Nav": "github", "Error": errMsg,
	})
}

// retryParkedOutboxRow returns one parked outbox row to the queue. Nothing
// else in the product resets GitHubOutbox.Attempts, so without this a row
// that exhausted MaxAttempts was undeliverable for the life of the
// deployment (finding I6).
//
// An unknown or already-retried id redirects back to the page rather than
// erroring: the button is rendered from a list that may be a few seconds
// stale, and a double click must not look like a fault.
func (h *Handlers) retryParkedOutboxRow(w http.ResponseWriter, r *http.Request) {
	id64, err := strconv.ParseUint(r.PathValue("id"), 10, 64)
	if err != nil || id64 == 0 {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	retried, err := ghsync.RetryParkedRow(h.DB, uint(id64))
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if retried {
		log.Printf("ui: outbox row %d un-parked by an operator", id64)
	}
	http.Redirect(w, r, "/settings/github", http.StatusSeeOther)
}

// validateTriggerLabel rejects a trigger label inside the golem:* namespace
// Golem owns for phase labels.
//
// applyPhaseLabel removes every golem:* label from an issue except the one
// it is about to apply, deliberately leaving the no-colon default "golem"
// and every human label alone. A trigger label that is itself inside that
// namespace — "golem:triage", say — is therefore stripped on the issue's
// first phase transition, silently un-enrolling it from the very filter that
// brought it in. Verified against the real Drain + applyPhaseLabel:
// trigger="golem:triage" on labels [golem:triage bug golem:brainstorm] left
// [bug golem:implement].
//
// Only that exact prefix is rejected. The default "golem" and near-misses
// like "golem-adjacent", "golemite", "Golem" and "team:golem" are outside the
// namespace, work correctly today, and must keep working.
func validateTriggerLabel(label string) error {
	if strings.HasPrefix(label, ghsync.PhaseLabelPrefix) {
		return fmt.Errorf("trigger label %q is inside the %s namespace Golem uses for phase labels, "+
			"so it would be removed from the issue on its first phase change — "+
			"un-enrolling it from its own trigger. Choose a label outside that namespace "+
			"(the default is %q).", label, ghsync.PhaseLabelPrefix, "golem")
	}
	return nil
}

// githubSettingsSubmit upserts one repo's settings from the form.
func (h *Handlers) githubSettingsSubmit(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	// r.PostForm, not r.FormValue: ParseForm merges the URL query into
	// r.Form for a POST, and nothing about this endpoint should be settable
	// from a link.
	remote := urlnorm.Normalize(r.PostForm.Get("repo_remote"))
	if remote == "" {
		http.Error(w, "repo_remote is required", http.StatusBadRequest)
		return
	}
	label := strings.TrimSpace(r.PostForm.Get("label"))
	if label != "" {
		if err := validateTriggerLabel(label); err != nil {
			h.renderGitHubSettings(w, r, err.Error())
			return
		}
	}
	owner, name := splitRemote(remote)

	var repo db.GitHubRepo
	err := h.DB.Where("repo_remote = ?", remote).First(&repo).Error
	switch {
	case errors.Is(err, gorm.ErrRecordNotFound):
		repo = db.GitHubRepo{RepoRemote: remote, Owner: owner, Name: name, Label: "golem"}
	case err != nil:
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	// A checkbox that is off is simply absent from the form body.
	repo.Enabled = r.PostForm.Get("enabled") != ""
	if label != "" {
		repo.Label = label
	}
	if err := h.DB.Save(&repo).Error; err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/settings/github", http.StatusSeeOther)
}

// splitRemote extracts owner and repo name from a normalized GitHub URL such
// as https://github.com/org/repo. Unparseable input yields empty strings.
func splitRemote(remote string) (string, string) {
	parts := strings.Split(strings.TrimSuffix(remote, "/"), "/")
	if len(parts) < 2 {
		return "", ""
	}
	return parts[len(parts)-2], parts[len(parts)-1]
}
