package ui

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/leonp92/golem/internal/orchestrator/admin"
	"github.com/leonp92/golem/internal/orchestrator/auth"
)

// renderSettings is the single entry into the settings shell so the sidebar
// active-state, banner rendering, and shared render-map keys (like
// MinPasswordLen) stay consistent across sections.
func (h *Handlers) renderSettings(w http.ResponseWriter, r *http.Request, section, errMsg, okMsg string) {
	data := h.base(r, "settings")
	data["Section"] = section
	data["Error"] = errMsg
	data["Success"] = okMsg
	data["MinPasswordLen"] = admin.MinPasswordLen
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
// password is required so a hijacked session cannot silently rotate the
// credential and lock the real user out.
func (h *Handlers) settingsChangePassword(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	u := auth.SessionUser(r) // guaranteed non-nil by authRoute → auth.RequireSession
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
		h.renderSettings(w, r, "security", fmt.Sprintf("New password must be at least %d characters.", admin.MinPasswordLen), "")
	default:
		h.renderSettings(w, r, "security", "Failed to update password.", "")
	}
}
