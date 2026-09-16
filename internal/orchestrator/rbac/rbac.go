// Package rbac defines the orchestrator's role-based access control policy:
// the permission constants, the static role -> permission table, and the HTTP
// middleware that enforces it. The policy is deliberately a compile-time table
// rather than a policy engine; Can is the single seam to swap if policy ever
// needs to be runtime-editable.
package rbac

import (
	"net/http"

	"github.com/leonp92/golem/internal/orchestrator/auth"
	"github.com/leonp92/golem/internal/orchestrator/db"
)

// Role is a named set of permissions, stored in db.User.Role.
type Role string

// Permission is a single capability a route or handler requires.
type Permission string

const (
	RoleAdmin     Role = "admin"
	RoleDeveloper Role = "developer"
)

const (
	PermUserManage   Permission = "user:manage" // create/list/delete users, change roles
	PermShemManage   Permission = "shem:manage" // register/remove shems (= repositories)
	PermShemView     Permission = "shem:view"   // read the /shems page and GET /api/shems
	PermTicketCreate Permission = "ticket:create"
	PermTicketView   Permission = "ticket:view"
	PermTicketManage Permission = "ticket:manage" // approve / requeue / close / answer / request-changes
)

// rolePermissions is the single source of truth for the policy. Adding a role
// means adding an entry here and nothing else. Deny by default: a role absent
// from this map has zero permissions.
var rolePermissions = map[Role][]Permission{
	RoleAdmin: {
		PermUserManage, PermShemManage, PermShemView,
		PermTicketCreate, PermTicketView, PermTicketManage,
	},
	RoleDeveloper: {
		PermShemView, PermTicketCreate, PermTicketView, PermTicketManage,
	},
}

// roleOrder is the stable display order used by Roles(), for UI dropdowns and
// CLI error messages.
var roleOrder = []Role{RoleAdmin, RoleDeveloper}

// Roles returns all known roles in a stable order.
func Roles() []Role { return append([]Role(nil), roleOrder...) }

// ParseRole validates a role string against the table. It is exact-match and
// case-sensitive; every write path (CLI, UI form, auto-provision) must call it.
func ParseRole(s string) (Role, bool) {
	r := Role(s)
	if _, ok := rolePermissions[r]; !ok {
		return "", false
	}
	return r, true
}

// Can reports whether u holds permission p. A nil user or a role that is not
// in the table (e.g. hand-edited in the database) holds nothing.
func Can(u *db.User, p Permission) bool {
	if u == nil {
		return false
	}
	for _, have := range rolePermissions[Role(u.Role)] {
		if have == p {
			return true
		}
	}
	return false
}

// Require is middleware that admits a request only if the session user in the
// request context holds p. It must be applied *inside* auth.RequireSession.
// A nil context user means the route was mis-wired and is refused rather than
// passed through.
func Require(p Permission) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !Can(auth.SessionUser(r), p) {
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
