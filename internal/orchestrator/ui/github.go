package ui

import (
	"errors"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/leonp92/golem/internal/orchestrator/db"
	"github.com/leonp92/golem/internal/orchestrator/ghsync"
	"github.com/leonp92/golem/internal/orchestrator/urlnorm"
	"github.com/leonp92/golem/internal/orchestrator/ws"
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
			RepoRemote: remote, Owner: owner, Name: name, Label: h.defaultLabel(),
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
	// Built from h.base like every other page: it supplies the identity and
	// permission flags the nav gates its controls on. This page used a
	// literal map — it predates base() — so an admin viewing it lost the
	// Users link, the New Ticket button and their own account menu, with no
	// error to indicate why. ui/nav_test.go now asserts this for every
	// nav-bearing route.
	data := h.base(r, "github")
	data["Repos"] = repos
	data["Parked"] = parked
	data["Error"] = errMsg
	h.render(w, r, "github_settings", data)
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

// buildGraph asks a shem serving this repository to run `golem graph build`.
//
// The orchestrator cannot do this itself — only the shem has the checkout —
// so the button claims the slot in the database and pushes a message over the
// websocket the shem already holds. Hub.Broadcast targets shems by the repos
// they declared at registration, so a shem that does not serve this remote
// never sees it.
//
// Fire and record, not fire and forget: a build over a large repository takes
// minutes and there is no ticket to stream into, so the outcome lands on the
// repo row and the page shows it.
func (h *Handlers) buildGraph(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	// PostForm, not FormValue: nothing here should be settable from a link.
	remote := urlnorm.Normalize(r.PostForm.Get("repo_remote"))
	if remote == "" {
		http.Error(w, "repo_remote is required", http.StatusBadRequest)
		return
	}

	repo, err := ghsync.StartGraphBuild(h.DB, remote, time.Now().UTC())
	switch {
	case errors.Is(err, ghsync.ErrGraphBuildRunning):
		// Not an error worth a page: the operator double-clicked, or a
		// colleague got there first. The row already says it is building.
		h.renderGitHubSettings(w, r, "A graph build is already running for "+repo.Owner+"/"+repo.Name+".")
		return
	case errors.Is(err, ghsync.ErrNoRepo):
		h.renderGitHubSettings(w, r, "Enable that repository before building its graph.")
		return
	case err != nil:
		log.Printf("ui: start graph build for %s: %v", remote, err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	if h.Hub == nil {
		// Recorded as failed rather than left reading "building…" for the
		// timeout: nothing is going to pick this up.
		if ferr := ghsync.FinishGraphBuild(h.DB, remote,
			"the orchestrator has no websocket hub, so no shem could be asked", time.Now().UTC()); ferr != nil {
			log.Printf("ui: recording the unreachable-hub graph build failure: %v", ferr)
		}
		h.renderGitHubSettings(w, r, "No shem connection is available to run the build.")
		return
	}
	h.Hub.Broadcast(remote, ws.WSMessage{Type: "graph_build", Repo: remote})
	log.Printf("ui: graph build requested for %s", remote)
	http.Redirect(w, r, "/settings/github", http.StatusSeeOther)
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
		if err := ghsync.ValidateTriggerLabel(label); err != nil {
			h.renderGitHubSettings(w, r, err.Error())
			return
		}
	}
	owner, name := splitRemote(remote)

	// Find rather than First: "no row yet" is the NORMAL case here — it is
	// what every first save of a shem-declared repo looks like — and First
	// logs a red "record not found" at error level for it. An operator
	// enabling their first repository should not see what reads like a
	// failure in the log at the exact moment it worked.
	var found []db.GitHubRepo
	if err := h.DB.Where("repo_remote = ?", remote).Limit(1).Find(&found).Error; err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	repo := db.GitHubRepo{RepoRemote: remote, Owner: owner, Name: name, Label: h.defaultLabel()}
	if len(found) > 0 {
		repo = found[0]
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
