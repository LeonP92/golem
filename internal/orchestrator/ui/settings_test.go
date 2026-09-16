package ui_test

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/leonp92/golem/internal/orchestrator/admin"
	"github.com/leonp92/golem/internal/orchestrator/db"
	"github.com/leonp92/golem/internal/orchestrator/rbac"
	"gorm.io/gorm"
)

// seedUISessionWithPassword creates a real bcrypt-hashed user (via the
// admin package, not the "x" dummy hash the plain seedUISession helper uses)
// so /settings/security/password can verify the "current password" input.
func seedUISessionWithPassword(t *testing.T, gdb *gorm.DB, mux *http.ServeMux, username, password string, role rbac.Role) (db.User, *http.Cookie) {
	t.Helper()
	if err := admin.UsersAddOrUpdate(gdb, username, password, string(role)); err != nil {
		t.Fatalf("UsersAddOrUpdate: %v", err)
	}
	var u db.User
	if err := gdb.Where("username = ?", username).First(&u).Error; err != nil {
		t.Fatalf("find %s: %v", username, err)
	}
	// Sign in via the real /login flow so the cookie is genuine.
	form := fmt.Sprintf("username=%s&password=%s", username, password)
	w := do(mux, "POST", "/login", nil, form)
	if len(w.Result().Cookies()) == 0 {
		t.Fatalf("login for %s produced no cookie (status %d)", username, w.Code)
	}
	return u, w.Result().Cookies()[0]
}

func TestSettingsIndex_RedirectsToDefaultSection(t *testing.T) {
	_, mux, gdb := newUIMux(t)
	_, cookie := seedUISessionWithPassword(t, gdb, mux, "leon", "originalpass", rbac.RoleDeveloper)

	w := do(mux, "GET", "/settings", cookie, "")
	if w.Code != http.StatusFound {
		t.Fatalf("got %d, want 302", w.Code)
	}
	if got := w.Header().Get("Location"); got != "/settings/security" {
		t.Errorf("got redirect to %q, want /settings/security", got)
	}
}

func TestSettingsSecurity_RendersForAnyAuthenticatedUser(t *testing.T) {
	_, mux, gdb := newUIMux(t)
	// A developer (no user:manage permission) must still reach /settings.
	_, cookie := seedUISessionWithPassword(t, gdb, mux, "leon", "originalpass", rbac.RoleDeveloper)

	w := do(mux, "GET", "/settings/security", cookie, "")
	if w.Code != http.StatusOK {
		t.Fatalf("got %d, want 200", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, "Change password") {
		t.Error("expected the change-password heading in the response")
	}
	// The sidebar shell must render around the section so future sections can be added.
	if !strings.Contains(body, `href="/settings/security"`) {
		t.Error("expected the Security sidebar link in the response")
	}
}

func TestSettingsSecurity_RequiresSession(t *testing.T) {
	_, mux, _ := newUIMux(t)
	w := do(mux, "GET", "/settings/security", nil, "")
	if w.Code != http.StatusFound {
		t.Fatalf("got %d, want 302 (redirect to /login)", w.Code)
	}
}

func TestSettingsChangePassword_HappyPath(t *testing.T) {
	_, mux, gdb := newUIMux(t)
	_, cookie := seedUISessionWithPassword(t, gdb, mux, "leon", "originalpass", rbac.RoleDeveloper)

	form := "current_password=originalpass&new_password=newerpassword&confirm_password=newerpassword"
	w := do(mux, "POST", "/settings/security/password", cookie, form)
	if w.Code != http.StatusOK {
		t.Fatalf("got %d, want 200", w.Code)
	}
	if !strings.Contains(w.Body.String(), "Password updated.") {
		t.Errorf("expected success banner, body: %s", w.Body.String())
	}
	// A subsequent login with the new password must succeed; the old password must not.
	if got := do(mux, "POST", "/login", nil, "username=leon&password=newerpassword"); len(got.Result().Cookies()) == 0 {
		t.Errorf("new password did not take effect (login failed)")
	}
	if got := do(mux, "POST", "/login", nil, "username=leon&password=originalpass"); len(got.Result().Cookies()) != 0 {
		t.Errorf("old password still accepted after change")
	}
}

func TestSettingsChangePassword_WrongCurrent(t *testing.T) {
	_, mux, gdb := newUIMux(t)
	_, cookie := seedUISessionWithPassword(t, gdb, mux, "leon", "originalpass", rbac.RoleDeveloper)

	form := "current_password=wrongguess&new_password=newerpassword&confirm_password=newerpassword"
	w := do(mux, "POST", "/settings/security/password", cookie, form)
	if !strings.Contains(w.Body.String(), "Current password is incorrect.") {
		t.Errorf("expected wrong-current error, body: %s", w.Body.String())
	}
}

func TestSettingsChangePassword_Mismatch(t *testing.T) {
	_, mux, gdb := newUIMux(t)
	_, cookie := seedUISessionWithPassword(t, gdb, mux, "leon", "originalpass", rbac.RoleDeveloper)

	form := "current_password=originalpass&new_password=newerpassword&confirm_password=different12345"
	w := do(mux, "POST", "/settings/security/password", cookie, form)
	if !strings.Contains(w.Body.String(), "New passwords do not match.") {
		t.Errorf("expected mismatch error, body: %s", w.Body.String())
	}
}

func TestSettingsChangePassword_TooShort(t *testing.T) {
	_, mux, gdb := newUIMux(t)
	_, cookie := seedUISessionWithPassword(t, gdb, mux, "leon", "originalpass", rbac.RoleDeveloper)

	form := "current_password=originalpass&new_password=abc&confirm_password=abc"
	w := do(mux, "POST", "/settings/security/password", cookie, form)
	if !strings.Contains(w.Body.String(), "at least 8 characters") {
		t.Errorf("expected weak-password error, body: %s", w.Body.String())
	}
}

func TestLogout_RequiresPost(t *testing.T) {
	_, mux, gdb := newUIMux(t)
	_, cookie := seedUISessionWithPassword(t, gdb, mux, "leon", "originalpass", rbac.RoleDeveloper)

	// GET must NOT invoke the logout handler — <img src="/logout"> on a hostile
	// page would otherwise sign the user out on load. A subsequent authenticated
	// GET must still succeed, proving the session survived the cross-site probe.
	do(mux, "GET", "/logout", cookie, "")
	if w := do(mux, "GET", "/settings/security", cookie, ""); w.Code != http.StatusOK {
		t.Fatalf("after GET /logout, session was cleared (got %d on /settings/security)", w.Code)
	}

	// POST clears the cookie and redirects to /login.
	w := do(mux, "POST", "/logout", cookie, "")
	if w.Code != http.StatusFound {
		t.Fatalf("POST /logout: got %d, want 302", w.Code)
	}
	var cleared bool
	for _, c := range w.Result().Cookies() {
		if c.Name == "golem_session" && c.MaxAge < 0 {
			cleared = true
			break
		}
	}
	if !cleared {
		t.Error("POST /logout did not emit a Set-Cookie clearing golem_session")
	}
}

func TestUsersPage_HidesDeleteButtonOnSelfRow(t *testing.T) {
	_, mux, gdb := newUIMux(t)
	me, cookie := seedUISessionWithPassword(t, gdb, mux, "root", "originalpass", rbac.RoleAdmin)
	// Add a second admin so the "you" row isn't the only row.
	if err := admin.UsersAddOrUpdate(gdb, "other", "otherpass1", string(rbac.RoleAdmin)); err != nil {
		t.Fatalf("seed other: %v", err)
	}

	w := do(mux, "GET", "/users", cookie, "")
	if w.Code != http.StatusOK {
		t.Fatalf("got %d, want 200", w.Code)
	}
	body := w.Body.String()

	// The self row's delete form must be absent; the other user's must be present.
	// URL-scoped assertions keep the test independent of daisyUI/Tailwind styling.
	var other db.User
	if err := gdb.Where("username = ?", "other").First(&other).Error; err != nil {
		t.Fatalf("find other: %v", err)
	}
	selfDelete := fmt.Sprintf(`action="/users/%d/delete"`, me.ID)
	otherDelete := fmt.Sprintf(`action="/users/%d/delete"`, other.ID)
	if strings.Contains(body, selfDelete) {
		t.Errorf("expected no self-delete form, but found %q", selfDelete)
	}
	if !strings.Contains(body, otherDelete) {
		t.Errorf("expected other-user delete form %q to be present", otherDelete)
	}
	if !strings.Contains(body, ">you</span>") {
		t.Error("expected a 'you' badge on the self row")
	}
}
