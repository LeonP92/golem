package admin_test

import (
	"errors"
	"net/http/httptest"
	"testing"

	"gorm.io/gorm"

	"github.com/leonp92/golem/internal/orchestrator/admin"
	"github.com/leonp92/golem/internal/orchestrator/auth"
	"github.com/leonp92/golem/internal/orchestrator/db"
	"github.com/leonp92/golem/internal/orchestrator/rbac"
)

func openTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	gdb, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	return gdb
}

// seedUser creates a user with an explicit role; db.Open's bootstrap only runs
// at open time, so roles here are entirely under the test's control.
func seedUser(t *testing.T, gdb *gorm.DB, username string, role rbac.Role) db.User {
	t.Helper()
	u := db.User{Username: username, PasswordHash: "x", Role: string(role)}
	if err := gdb.Create(&u).Error; err != nil {
		t.Fatalf("create %s: %v", username, err)
	}
	return u
}

func roleOf(t *testing.T, gdb *gorm.DB, username string) string {
	t.Helper()
	var u db.User
	if err := gdb.Where("username = ?", username).First(&u).Error; err != nil {
		t.Fatalf("find %s: %v", username, err)
	}
	return u.Role
}

func TestUsersAdd_RejectsUnknownRole(t *testing.T) {
	gdb := openTestDB(t)
	err := admin.UsersAdd(gdb, "bogus", "root")
	if !errors.Is(err, admin.ErrUnknownRole) {
		t.Fatalf("got %v, want ErrUnknownRole", err)
	}
	var count int64
	gdb.Model(&db.User{}).Count(&count)
	if count != 0 {
		t.Errorf("got %d users, want none created", count)
	}
}

func TestUsersAddOrUpdate_UpdatesRoleOnExistingUser(t *testing.T) {
	gdb := openTestDB(t)
	seedUser(t, gdb, "leon", rbac.RoleDeveloper)

	if err := admin.UsersAddOrUpdate(gdb, "leon", "newpassword", string(rbac.RoleAdmin)); err != nil {
		t.Fatalf("UsersAddOrUpdate: %v", err)
	}
	if got := roleOf(t, gdb, "leon"); got != string(rbac.RoleAdmin) {
		t.Errorf("got role %q, want admin", got)
	}
}

func TestUsersList_OrderedByUsername(t *testing.T) {
	gdb := openTestDB(t)
	seedUser(t, gdb, "carol", rbac.RoleDeveloper)
	seedUser(t, gdb, "alice", rbac.RoleAdmin)
	seedUser(t, gdb, "bob", rbac.RoleDeveloper)

	users, err := admin.UsersList(gdb)
	if err != nil {
		t.Fatalf("UsersList: %v", err)
	}
	want := []string{"alice", "bob", "carol"}
	if len(users) != len(want) {
		t.Fatalf("got %d users, want %d", len(users), len(want))
	}
	for i, name := range want {
		if users[i].Username != name {
			t.Errorf("position %d: got %q, want %q", i, users[i].Username, name)
		}
	}
}

func TestUsersSetRole_RefusesDemotingLastAdmin(t *testing.T) {
	gdb := openTestDB(t)
	seedUser(t, gdb, "root", rbac.RoleAdmin)
	seedUser(t, gdb, "dev1", rbac.RoleDeveloper)

	err := admin.UsersSetRole(gdb, "root", string(rbac.RoleDeveloper))
	if !errors.Is(err, admin.ErrLastAdmin) {
		t.Fatalf("got %v, want ErrLastAdmin", err)
	}
	if got := roleOf(t, gdb, "root"); got != string(rbac.RoleAdmin) {
		t.Errorf("got role %q, want the database unchanged (admin)", got)
	}
}

func TestUsersSetRole_AllowsDemotionWhenAnotherAdminExists(t *testing.T) {
	gdb := openTestDB(t)
	seedUser(t, gdb, "root", rbac.RoleAdmin)
	seedUser(t, gdb, "dev1", rbac.RoleAdmin)

	if err := admin.UsersSetRole(gdb, "root", string(rbac.RoleDeveloper)); err != nil {
		t.Fatalf("UsersSetRole: %v", err)
	}
	if got := roleOf(t, gdb, "root"); got != string(rbac.RoleDeveloper) {
		t.Errorf("got role %q, want developer", got)
	}
}

func TestUsersSetRole_RejectsUnknownRole(t *testing.T) {
	gdb := openTestDB(t)
	seedUser(t, gdb, "dev1", rbac.RoleDeveloper)

	if err := admin.UsersSetRole(gdb, "dev1", "root"); !errors.Is(err, admin.ErrUnknownRole) {
		t.Fatalf("got %v, want ErrUnknownRole", err)
	}
	if got := roleOf(t, gdb, "dev1"); got != string(rbac.RoleDeveloper) {
		t.Errorf("got role %q, want the database unchanged (developer)", got)
	}
}

func TestUsersRemove_RefusesRemovingLastAdmin(t *testing.T) {
	gdb := openTestDB(t)
	seedUser(t, gdb, "root", rbac.RoleAdmin)
	seedUser(t, gdb, "dev1", rbac.RoleDeveloper)

	err := admin.UsersRemove(gdb, "root")
	if !errors.Is(err, admin.ErrLastAdmin) {
		t.Fatalf("got %v, want ErrLastAdmin", err)
	}
	var count int64
	gdb.Model(&db.User{}).Count(&count)
	if count != 2 {
		t.Errorf("got %d users, want 2 (database unchanged)", count)
	}
}

func TestUsersRemove_DeletesSessions(t *testing.T) {
	gdb := openTestDB(t)
	seedUser(t, gdb, "root", rbac.RoleAdmin)
	dev := seedUser(t, gdb, "dev1", rbac.RoleDeveloper)
	if err := auth.CreateSession(gdb, httptest.NewRecorder(), dev.ID, false); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	if err := admin.UsersRemove(gdb, "dev1"); err != nil {
		t.Fatalf("UsersRemove: %v", err)
	}
	var sessions int64
	gdb.Model(&db.Session{}).Where("user_id = ?", dev.ID).Count(&sessions)
	if sessions != 0 {
		t.Errorf("got %d sessions, want 0", sessions)
	}
}

func TestUsersRemove_UnknownUser(t *testing.T) {
	gdb := openTestDB(t)
	if err := admin.UsersRemove(gdb, "nobody"); err == nil {
		t.Fatal("expected an error for a user that does not exist")
	}
}

func TestValidatePassword_LengthRule(t *testing.T) {
	cases := []struct {
		in      string
		wantErr bool
	}{
		{"", true},
		{"short", true},
		{"1234567", true},
		{"12345678", false},
		{"longerpassphrase", false},
	}
	for _, tc := range cases {
		err := admin.ValidatePassword(tc.in)
		if (err != nil) != tc.wantErr {
			t.Errorf("ValidatePassword(%q) err=%v, wantErr=%v", tc.in, err, tc.wantErr)
		}
		if tc.wantErr && err != nil && !errors.Is(err, admin.ErrWeakPassword) {
			t.Errorf("ValidatePassword(%q) = %v, want ErrWeakPassword", tc.in, err)
		}
	}
}

func TestUsersAddOrUpdate_RejectsWeakPassword(t *testing.T) {
	gdb := openTestDB(t)
	err := admin.UsersAddOrUpdate(gdb, "shorty", "abc", string(rbac.RoleAdmin))
	if !errors.Is(err, admin.ErrWeakPassword) {
		t.Fatalf("got %v, want ErrWeakPassword", err)
	}
	var count int64
	gdb.Model(&db.User{}).Count(&count)
	if count != 0 {
		t.Errorf("got %d users, want 0 (weak password must not persist)", count)
	}
}

func TestChangePassword_HappyPath(t *testing.T) {
	gdb := openTestDB(t)
	if err := admin.UsersAddOrUpdate(gdb, "leon", "originalpass", string(rbac.RoleAdmin)); err != nil {
		t.Fatalf("UsersAddOrUpdate: %v", err)
	}
	var u db.User
	if err := gdb.Where("username = ?", "leon").First(&u).Error; err != nil {
		t.Fatalf("find user: %v", err)
	}

	if err := admin.ChangePassword(gdb, u.ID, "originalpass", "newerpassword"); err != nil {
		t.Fatalf("ChangePassword: %v", err)
	}

	// Confirm the old password no longer works and the new one does.
	if err := admin.ChangePassword(gdb, u.ID, "originalpass", "irrelevantxxx"); !errors.Is(err, admin.ErrWrongPassword) {
		t.Errorf("old password still accepted; got %v", err)
	}
	if err := admin.ChangePassword(gdb, u.ID, "newerpassword", "thirdpassword"); err != nil {
		t.Errorf("new password should be accepted; got %v", err)
	}
}

func TestChangePassword_RejectsWrongOld(t *testing.T) {
	gdb := openTestDB(t)
	if err := admin.UsersAddOrUpdate(gdb, "leon", "originalpass", string(rbac.RoleAdmin)); err != nil {
		t.Fatalf("UsersAddOrUpdate: %v", err)
	}
	var u db.User
	gdb.Where("username = ?", "leon").First(&u)

	err := admin.ChangePassword(gdb, u.ID, "wrongguess", "somethinglong")
	if !errors.Is(err, admin.ErrWrongPassword) {
		t.Fatalf("got %v, want ErrWrongPassword", err)
	}
}

func TestChangePassword_RejectsWeakNew(t *testing.T) {
	gdb := openTestDB(t)
	if err := admin.UsersAddOrUpdate(gdb, "leon", "originalpass", string(rbac.RoleAdmin)); err != nil {
		t.Fatalf("UsersAddOrUpdate: %v", err)
	}
	var u db.User
	gdb.Where("username = ?", "leon").First(&u)

	err := admin.ChangePassword(gdb, u.ID, "originalpass", "abc")
	if !errors.Is(err, admin.ErrWeakPassword) {
		t.Fatalf("got %v, want ErrWeakPassword", err)
	}
}
