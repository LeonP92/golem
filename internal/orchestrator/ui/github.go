package ui

import (
	"errors"
	"net/http"
	"strings"

	"github.com/leonp92/golem/internal/orchestrator/db"
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
	h.render(w, r, "github_settings", map[string]any{"Repos": repos, "Nav": "github"})
}

// githubSettingsSubmit upserts one repo's settings from the form.
func (h *Handlers) githubSettingsSubmit(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	remote := urlnorm.Normalize(r.FormValue("repo_remote"))
	if remote == "" {
		http.Error(w, "repo_remote is required", http.StatusBadRequest)
		return
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
	repo.Enabled = r.FormValue("enabled") != ""
	if label := strings.TrimSpace(r.FormValue("label")); label != "" {
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
