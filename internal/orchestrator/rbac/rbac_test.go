package rbac_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"gorm.io/gorm"

	"github.com/leonp92/golem/internal/orchestrator/auth"
	"github.com/leonp92/golem/internal/orchestrator/db"
	"github.com/leonp92/golem/internal/orchestrator/rbac"
)

// allPermissions lists every permission constant, so a permission nobody put
// in the role table shows up as a failure here rather than as a silent deny.
var allPermissions = []rbac.Permission{
	rbac.PermUserManage,
	rbac.PermShemManage,
	rbac.PermShemView,
	rbac.PermTicketCreate,
	rbac.PermTicketView,
	rbac.PermTicketManage,
}

func TestCan_Admin(t *testing.T) {
	u := &db.User{Role: string(rbac.RoleAdmin)}
	for _, p := range allPermissions {
		if !rbac.Can(u, p) {
			t.Errorf("admin should hold %q", p)
		}
	}
}

func TestCan_Developer(t *testing.T) {
	u := &db.User{Role: string(rbac.RoleDeveloper)}
	want := map[rbac.Permission]bool{
		rbac.PermShemView:     true,
		rbac.PermTicketCreate: true,
		rbac.PermTicketView:   true,
		rbac.PermTicketManage: true,
		rbac.PermUserManage:   false,
		rbac.PermShemManage:   false,
	}
	for _, p := range allPermissions {
		if got := rbac.Can(u, p); got != want[p] {
			t.Errorf("developer %q: got %v, want %v", p, got, want[p])
		}
	}
}

func TestCan_UnknownRole(t *testing.T) {
	u := &db.User{Role: "root"}
	for _, p := range allPermissions {
		if rbac.Can(u, p) {
			t.Errorf("unknown role should not hold %q", p)
		}
	}
}

func TestCan_NilUser(t *testing.T) {
	for _, p := range allPermissions {
		if rbac.Can(nil, p) {
			t.Errorf("nil user should not hold %q", p)
		}
	}
}

func TestParseRole(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want rbac.Role
		ok   bool
	}{
		{"admin", rbac.RoleAdmin, true},
		{"developer", rbac.RoleDeveloper, true},
		{"", "", false},
		{"Admin", "", false},
		{"root", "", false},
	} {
		got, ok := rbac.ParseRole(tc.in)
		if ok != tc.ok || got != tc.want {
			t.Errorf("ParseRole(%q) = (%q, %v), want (%q, %v)", tc.in, got, ok, tc.want, tc.ok)
		}
	}
}

func TestRoles_Order(t *testing.T) {
	got := rbac.Roles()
	want := []rbac.Role{rbac.RoleAdmin, rbac.RoleDeveloper}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}

// sessionFor creates a user with the given role and an authenticated session
// cookie for it, so the middleware tests exercise the real context plumbing.
func sessionFor(t *testing.T, role string) (*gorm.DB, *http.Cookie) {
	t.Helper()
	gdb, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	user := db.User{Username: "u", PasswordHash: "x", Role: role}
	if err := gdb.Create(&user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	w := httptest.NewRecorder()
	if err := auth.CreateSession(gdb, w, user.ID, false); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	return gdb, w.Result().Cookies()[0]
}

// countingHandler records whether it was reached.
func countingHandler(called *bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*called = true
		w.WriteHeader(http.StatusOK)
	})
}

func TestRequire_Permitted(t *testing.T) {
	gdb, cookie := sessionFor(t, string(rbac.RoleAdmin))
	called := false
	h := auth.RequireSession(gdb)(rbac.Require(rbac.PermUserManage)(countingHandler(&called)))

	req := httptest.NewRequest("GET", "/users", nil)
	req.AddCookie(cookie)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("got %d, want 200", w.Code)
	}
	if !called {
		t.Error("handler was not called")
	}
}

func TestRequire_Forbidden(t *testing.T) {
	gdb, cookie := sessionFor(t, string(rbac.RoleDeveloper))
	called := false
	h := auth.RequireSession(gdb)(rbac.Require(rbac.PermUserManage)(countingHandler(&called)))

	req := httptest.NewRequest("GET", "/users", nil)
	req.AddCookie(cookie)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Errorf("got %d, want 403", w.Code)
	}
	if called {
		t.Error("handler was called despite missing permission")
	}
}

func TestRequire_NoContextUser(t *testing.T) {
	called := false
	h := rbac.Require(rbac.PermTicketView)(countingHandler(&called))

	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/dashboard", nil))

	if w.Code != http.StatusForbidden {
		t.Errorf("got %d, want 403", w.Code)
	}
	if called {
		t.Error("handler was called with no session user in context")
	}
}
