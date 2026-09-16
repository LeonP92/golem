package ui

import (
	"errors"
	"net/http"
	"strconv"

	"golang.org/x/crypto/bcrypt"

	"github.com/leonp92/golem/internal/orchestrator/admin"
	"github.com/leonp92/golem/internal/orchestrator/auth"
	"github.com/leonp92/golem/internal/orchestrator/db"
	"github.com/leonp92/golem/internal/orchestrator/rbac"
)

// renderUsers renders the /users page with the current user list, the role
// options for the dropdowns, and an optional validation error.
func (h *Handlers) renderUsers(w http.ResponseWriter, r *http.Request, errMsg string) {
	users, err := admin.UsersList(h.DB)
	if err != nil {
		http.Error(w, "database error", http.StatusInternalServerError)
		return
	}
	roles := make([]string, 0, len(rbac.Roles()))
	for _, role := range rbac.Roles() {
		roles = append(roles, string(role))
	}
	data := h.base(r, "users")
	data["Users"] = users
	data["Roles"] = roles
	data["Error"] = errMsg
	h.render(w, "users", data)
}

func (h *Handlers) usersPage(w http.ResponseWriter, r *http.Request) {
	h.renderUsers(w, r, "")
}

// usersCreate validates and creates a user from the /users form.
func (h *Handlers) usersCreate(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	username := r.FormValue("username")
	password := r.FormValue("password")
	role, roleOK := rbac.ParseRole(r.FormValue("role"))
	switch {
	case username == "":
		h.renderUsers(w, r, "Username is required.")
		return
	case len(password) < 8:
		h.renderUsers(w, r, "Password must be at least 8 characters.")
		return
	case !roleOK:
		h.renderUsers(w, r, "Invalid role.")
		return
	}
	var existing db.User
	if h.DB.Where("username = ?", username).First(&existing).Error == nil {
		h.renderUsers(w, r, "Username already exists.")
		return
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		h.renderUsers(w, r, "Failed to create user.")
		return
	}
	if err := h.DB.Create(&db.User{
		Username: username, PasswordHash: string(hash), Role: string(role),
	}).Error; err != nil {
		h.renderUsers(w, r, "Failed to create user.")
		return
	}
	http.Redirect(w, r, "/users", http.StatusFound)
}

// usersSetRole changes a user's role, refusing to demote the last admin.
func (h *Handlers) usersSetRole(w http.ResponseWriter, r *http.Request) {
	target, ok := h.targetUser(w, r)
	if !ok {
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	role := r.FormValue("role")
	if _, ok := rbac.ParseRole(role); !ok {
		h.renderUsers(w, r, "Invalid role.")
		return
	}
	if err := admin.UsersSetRole(h.DB, target.Username, role); err != nil {
		if errors.Is(err, admin.ErrLastAdmin) {
			h.renderUsers(w, r, "Cannot demote the last admin.")
			return
		}
		h.renderUsers(w, r, "Failed to change role.")
		return
	}
	http.Redirect(w, r, "/users", http.StatusFound)
}

// usersDelete deletes a user and their sessions. You cannot delete yourself,
// and you cannot delete the last admin.
func (h *Handlers) usersDelete(w http.ResponseWriter, r *http.Request) {
	target, ok := h.targetUser(w, r)
	if !ok {
		return
	}
	if me := auth.SessionUser(r); me != nil && me.ID == target.ID {
		h.renderUsers(w, r, "You cannot delete your own account.")
		return
	}
	if err := admin.UsersRemove(h.DB, target.Username); err != nil {
		if errors.Is(err, admin.ErrLastAdmin) {
			h.renderUsers(w, r, "Cannot delete the last admin.")
			return
		}
		h.renderUsers(w, r, "Failed to delete user.")
		return
	}
	http.Redirect(w, r, "/users", http.StatusFound)
}

// targetUser resolves the {id} path value to a db.User, writing a 404 and
// reporting false if it does not exist.
func (h *Handlers) targetUser(w http.ResponseWriter, r *http.Request) (db.User, bool) {
	id, err := strconv.ParseUint(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return db.User{}, false
	}
	var u db.User
	if err := h.DB.First(&u, uint(id)).Error; err != nil {
		http.Error(w, "user not found", http.StatusNotFound)
		return db.User{}, false
	}
	return u, true
}
