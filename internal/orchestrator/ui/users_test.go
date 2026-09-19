package ui_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"

	"github.com/leonp92/golem/internal/orchestrator/auth"
	"github.com/leonp92/golem/internal/orchestrator/db"
	"github.com/leonp92/golem/internal/orchestrator/rbac"
	"github.com/leonp92/golem/internal/orchestrator/ui"
)

// newUIMux builds a fully templated ui.Handlers and its mux. The /users page
// renders, so these tests cannot use ui.NewHandlers(gdb, nil).
func newUIMux(t *testing.T) (*ui.Handlers, *http.ServeMux, *gorm.DB) {
	t.Helper()
	gdb, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	tmpls, err := ui.LoadTemplates()
	if err != nil {
		t.Fatalf("LoadTemplates: %v", err)
	}
	h := ui.NewHandlersWithMap(gdb, tmpls, false)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	return h, mux, gdb
}

// seedUISession creates a user with the given role and returns it plus a
// session cookie. The role is always set explicitly, so no test depends on
// db.Open's admin bootstrap.
func seedUISession(t *testing.T, gdb *gorm.DB, username string, role rbac.Role) (db.User, *http.Cookie) {
	t.Helper()
	user := db.User{Username: username, PasswordHash: "x", Role: string(role)}
	if err := gdb.Create(&user).Error; err != nil {
		t.Fatalf("create user %s: %v", username, err)
	}
	w := httptest.NewRecorder()
	if err := auth.CreateSession(gdb, w, user.ID, false); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	return user, w.Result().Cookies()[0]
}

func do(mux *http.ServeMux, method, path string, cookie *http.Cookie, form string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(form))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if cookie != nil {
		req.AddCookie(cookie)
		// The user-management POSTs create accounts, change roles and delete
		// users, so they are behind RequireCSRF like every other
		// state-changing session route — an injected same-origin form in a
		// markdown sink would otherwise be a privilege escalation. A browser
		// sends this from the <meta> the layout publishes; the tests send it
		// the same way. Safe methods pass through without one.
		if method != http.MethodGet && method != http.MethodHead {
			req.Header.Set(auth.CSRFHeader, auth.CSRFTokenForSession(cookie.Value))
		}
	}
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	return w
}

func userCount(t *testing.T, gdb *gorm.DB) int64 {
	t.Helper()
	var n int64
	gdb.Model(&db.User{}).Count(&n)
	return n
}

func roleOf(t *testing.T, gdb *gorm.DB, username string) string {
	t.Helper()
	var u db.User
	if err := gdb.Where("username = ?", username).First(&u).Error; err != nil {
		t.Fatalf("find %s: %v", username, err)
	}
	return u.Role
}

func TestUsersRoutes_DeveloperForbidden(t *testing.T) {
	_, mux, gdb := newUIMux(t)
	dev, cookie := seedUISession(t, gdb, "dev1", rbac.RoleDeveloper)

	for _, tc := range []struct{ method, path, form string }{
		{"GET", "/users", ""},
		{"POST", "/users", "username=x&password=password123&role=developer"},
		{"POST", fmt.Sprintf("/users/%d/role", dev.ID), "role=admin"},
		{"POST", fmt.Sprintf("/users/%d/delete", dev.ID), ""},
	} {
		w := do(mux, tc.method, tc.path, cookie, tc.form)
		if w.Code != http.StatusForbidden {
			t.Errorf("%s %s: got %d, want 403", tc.method, tc.path, w.Code)
		}
	}
	if userCount(t, gdb) != 1 {
		t.Errorf("got %d users, want the database unchanged", userCount(t, gdb))
	}
}

func TestDeveloperCanReachTicketRoutes(t *testing.T) {
	_, mux, gdb := newUIMux(t)
	_, cookie := seedUISession(t, gdb, "dev1", rbac.RoleDeveloper)

	if w := do(mux, "GET", "/dashboard", cookie, ""); w.Code != http.StatusOK {
		t.Errorf("GET /dashboard: got %d, want 200", w.Code)
	}
	if w := do(mux, "GET", "/tickets/new", cookie, ""); w.Code != http.StatusOK {
		t.Errorf("GET /tickets/new: got %d, want 200", w.Code)
	}
	form := "repo_remote=https://github.com/org/repo&base_branch=main&title=T&description=d"
	if w := do(mux, "POST", "/tickets/new", cookie, form); w.Code != http.StatusFound {
		t.Errorf("POST /tickets/new: got %d, want 302", w.Code)
	}
	if w := do(mux, "GET", "/shems", cookie, ""); w.Code != http.StatusOK {
		t.Errorf("GET /shems: got %d, want 200", w.Code)
	}
}

func TestAdminCreatesUser_EitherRole(t *testing.T) {
	_, mux, gdb := newUIMux(t)
	_, cookie := seedUISession(t, gdb, "root", rbac.RoleAdmin)

	for _, role := range []rbac.Role{rbac.RoleDeveloper, rbac.RoleAdmin} {
		name := "new-" + string(role)
		form := fmt.Sprintf("username=%s&password=password123&role=%s", name, role)
		if w := do(mux, "POST", "/users", cookie, form); w.Code != http.StatusFound {
			t.Fatalf("POST /users role=%s: got %d, want 302: %s", role, w.Code, w.Body.String())
		}
		var created db.User
		if err := gdb.Where("username = ?", name).First(&created).Error; err != nil {
			t.Fatalf("user %s was not created: %v", name, err)
		}
		if created.Role != string(role) {
			t.Errorf("%s: got role %q, want %q", name, created.Role, role)
		}
		// The stored hash must verify the submitted password, i.e. the new user
		// can log in.
		if err := bcrypt.CompareHashAndPassword([]byte(created.PasswordHash), []byte("password123")); err != nil {
			t.Errorf("%s: stored hash does not verify the submitted password: %v", name, err)
		}
	}
}

func TestUsersPage_DeveloperSeesNoUsersNav(t *testing.T) {
	_, mux, gdb := newUIMux(t)
	_, adminCookie := seedUISession(t, gdb, "root", rbac.RoleAdmin)
	_, devCookie := seedUISession(t, gdb, "dev1", rbac.RoleDeveloper)

	devBody := do(mux, "GET", "/dashboard", devCookie, "").Body.String()
	if strings.Contains(devBody, `href="/users"`) {
		t.Error("developer dashboard should not link to /users")
	}
	adminBody := do(mux, "GET", "/dashboard", adminCookie, "").Body.String()
	if !strings.Contains(adminBody, `href="/users"`) {
		t.Error("admin dashboard should link to /users")
	}
}

func TestUsersCreate_Validation(t *testing.T) {
	_, mux, gdb := newUIMux(t)
	_, cookie := seedUISession(t, gdb, "root", rbac.RoleAdmin)

	for _, tc := range []struct{ name, form, want string }{
		{"empty username", "username=&password=password123&role=developer", "Username is required."},
		{"short password", "username=jane&password=1234567&role=developer", "Password must be at least 8 characters."},
		{"unknown role", "username=jane&password=password123&role=root", "Invalid role."},
		{"duplicate username", "username=root&password=password123&role=developer", "Username already exists."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := userCount(t, gdb)
			w := do(mux, "POST", "/users", cookie, tc.form)
			if w.Code != http.StatusOK {
				t.Fatalf("got %d, want 200 (re-render)", w.Code)
			}
			if !strings.Contains(w.Body.String(), tc.want) {
				t.Errorf("expected %q in the response body", tc.want)
			}
			if after := userCount(t, gdb); after != before {
				t.Errorf("got %d users, want %d (database unchanged)", after, before)
			}
		})
	}
}

func TestUsersDelete_RefusesSelf(t *testing.T) {
	_, mux, gdb := newUIMux(t)
	me, cookie := seedUISession(t, gdb, "root", rbac.RoleAdmin)
	seedUISession(t, gdb, "other", rbac.RoleAdmin)

	w := do(mux, "POST", fmt.Sprintf("/users/%d/delete", me.ID), cookie, "")
	if w.Code != http.StatusOK {
		t.Fatalf("got %d, want 200 (re-render)", w.Code)
	}
	if !strings.Contains(w.Body.String(), "You cannot delete your own account.") {
		t.Error("expected the self-delete refusal message")
	}
	if userCount(t, gdb) != 2 {
		t.Errorf("got %d users, want 2 (database unchanged)", userCount(t, gdb))
	}
}

// TestUsersDelete_RefusesLastAdmin deletes the only admin account. Only an
// admin may call this route, so the only session that can target the last admin
// is the last admin's own — which is exactly why the request is refused and the
// account survives. admin.UsersRemove's ErrLastAdmin guard (the CLI path, where
// a third party can target it) is covered in internal/orchestrator/admin.
func TestUsersDelete_RefusesLastAdmin(t *testing.T) {
	_, mux, gdb := newUIMux(t)
	root, cookie := seedUISession(t, gdb, "root", rbac.RoleAdmin)
	seedUISession(t, gdb, "dev1", rbac.RoleDeveloper)

	w := do(mux, "POST", fmt.Sprintf("/users/%d/delete", root.ID), cookie, "")
	if w.Code != http.StatusOK {
		t.Fatalf("got %d, want 200 (re-render)", w.Code)
	}
	if userCount(t, gdb) != 2 {
		t.Errorf("got %d users, want 2 (database unchanged)", userCount(t, gdb))
	}
	if got := roleOf(t, gdb, "root"); got != string(rbac.RoleAdmin) {
		t.Errorf("got role %q, want the last admin intact", got)
	}
}

func TestUsersDelete_UnknownUser(t *testing.T) {
	_, mux, gdb := newUIMux(t)
	_, cookie := seedUISession(t, gdb, "root", rbac.RoleAdmin)

	if w := do(mux, "POST", "/users/999/delete", cookie, ""); w.Code != http.StatusNotFound {
		t.Errorf("got %d, want 404", w.Code)
	}
}

func TestUsersSetRole_RefusesDemotingLastAdmin(t *testing.T) {
	_, mux, gdb := newUIMux(t)
	me, cookie := seedUISession(t, gdb, "root", rbac.RoleAdmin)
	seedUISession(t, gdb, "dev1", rbac.RoleDeveloper)

	w := do(mux, "POST", fmt.Sprintf("/users/%d/role", me.ID), cookie, "role=developer")
	if w.Code != http.StatusOK {
		t.Fatalf("got %d, want 200 (re-render)", w.Code)
	}
	if !strings.Contains(w.Body.String(), "Cannot demote the last admin.") {
		t.Error("expected the last-admin refusal message")
	}
	if got := roleOf(t, gdb, "root"); got != string(rbac.RoleAdmin) {
		t.Errorf("got role %q, want admin (database unchanged)", got)
	}
}

func TestUsersDelete_RevokesSessions(t *testing.T) {
	_, mux, gdb := newUIMux(t)
	_, adminCookie := seedUISession(t, gdb, "root", rbac.RoleAdmin)
	dev, devCookie := seedUISession(t, gdb, "dev1", rbac.RoleDeveloper)

	if w := do(mux, "POST", fmt.Sprintf("/users/%d/delete", dev.ID), adminCookie, ""); w.Code != http.StatusFound {
		t.Fatalf("delete: got %d, want 302: %s", w.Code, w.Body.String())
	}

	w := do(mux, "GET", "/dashboard", devCookie, "")
	if w.Code != http.StatusFound || w.Header().Get("Location") != "/login" {
		t.Errorf("deleted user's cookie: got %d -> %q, want 302 -> /login", w.Code, w.Header().Get("Location"))
	}
}

func TestRolePromotionTakesEffectImmediately(t *testing.T) {
	_, mux, gdb := newUIMux(t)
	_, adminCookie := seedUISession(t, gdb, "root", rbac.RoleAdmin)
	dev, devCookie := seedUISession(t, gdb, "dev1", rbac.RoleDeveloper)

	if w := do(mux, "GET", "/users", devCookie, ""); w.Code != http.StatusForbidden {
		t.Fatalf("before promotion: got %d, want 403", w.Code)
	}
	if w := do(mux, "POST", fmt.Sprintf("/users/%d/role", dev.ID), adminCookie, "role=admin"); w.Code != http.StatusFound {
		t.Fatalf("promote: got %d, want 302: %s", w.Code, w.Body.String())
	}
	// Same cookie, no re-login.
	if w := do(mux, "GET", "/users", devCookie, ""); w.Code != http.StatusOK {
		t.Errorf("after promotion: got %d, want 200", w.Code)
	}
}
