package ui

import (
	"errors"
	"net/http"

	"github.com/leonp92/golem/internal/orchestrator/admin"
	"github.com/leonp92/golem/internal/orchestrator/auth"
)

// renderSettings is the single entry into the settings shell so the sidebar
// active-state and banner rendering stay consistent across sections.
func (h *Handlers) renderSettings(w http.ResponseWriter, r *http.Request, section, errMsg, okMsg string) {
	data := h.base(r, "settings")
	data["Section"] = section
	data["Error"] = errMsg
	data["Success"] = okMsg
	h.render(w, "settings", data)
}

// settingsIndex redirects the bare /settings URL to the default section, so a
// nav link doesn't have to know which section is "first".
func (h *Handlers) settingsIndex(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, "/settings/security", http.StatusFound)
}

func (h *Handlers) settingsSecurity(w http.ResponseWriter, r *http.Request) {
	h.renderSettings(w, r, "security", "", "")
}

// settingsChangePassword processes the change-password form. The current
// password is required so that a hijacked session cannot silently rotate the
// credential and lock the real user out.
func (h *Handlers) settingsChangePassword(w http.ResponseWriter, r *http.Request) {
	u := auth.SessionUser(r)
	if u == nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	oldPass := r.FormValue("current_password")
	newPass := r.FormValue("new_password")
	confirm := r.FormValue("confirm_password")
	if newPass != confirm {
		h.renderSettings(w, r, "security", "New passwords do not match.", "")
		return
	}
	switch err := admin.ChangePassword(h.DB, u.ID, oldPass, newPass); {
	case err == nil:
		h.renderSettings(w, r, "security", "", "Password updated.")
	case errors.Is(err, admin.ErrWrongPassword):
		h.renderSettings(w, r, "security", "Current password is incorrect.", "")
	case errors.Is(err, admin.ErrWeakPassword):
		h.renderSettings(w, r, "security", "New password must be at least 8 characters.", "")
	default:
		h.renderSettings(w, r, "security", "Failed to update password.", "")
	}
}
