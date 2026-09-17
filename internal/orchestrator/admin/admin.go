package admin

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strings"

	"golang.org/x/crypto/bcrypt"
	"golang.org/x/term"

	"github.com/leonp92/golem/internal/orchestrator/db"
	"github.com/leonp92/golem/internal/orchestrator/rbac"
	"gorm.io/gorm"
)

// ErrLastAdmin is returned when an operation would leave the orchestrator with
// no admin user.
var ErrLastAdmin = errors.New("refusing to leave the orchestrator with no admin user")

// ErrUnknownRole is returned when a role string is not a known rbac role.
var ErrUnknownRole = errors.New("unknown role")

// ErrWeakPassword is returned when a password fails ValidatePassword.
var ErrWeakPassword = errors.New("password too weak")

// ErrWrongPassword is returned when a change-password call presents an old
// password that does not match the stored hash.
var ErrWrongPassword = errors.New("current password is incorrect")

// MinPasswordLen is the minimum accepted password length, enforced at every
// entry point (CLI, HTTP form, GOLEM_ADMIN_PASSWORD env var).
const MinPasswordLen = 8

// ValidatePassword returns ErrWeakPassword if pass is unacceptable. Keep this
// the single source of truth so the UI, CLI, and env-var seed can't drift.
func ValidatePassword(pass string) error {
	if len(pass) < MinPasswordLen {
		return fmt.Errorf("%w: must be at least %d characters", ErrWeakPassword, MinPasswordLen)
	}
	return nil
}

// wouldOrphanAdmins reports whether removing or demoting user u would leave the
// orchestrator with zero admins.
func wouldOrphanAdmins(gdb *gorm.DB, u db.User) (bool, error) {
	if u.Role != string(rbac.RoleAdmin) {
		return false, nil
	}
	var admins int64
	err := gdb.Model(&db.User{}).Where("role = ?", string(rbac.RoleAdmin)).Count(&admins).Error
	return admins <= 1, err
}

// roleList renders the valid roles for error messages, e.g. "admin, developer".
func roleList() string {
	names := make([]string, 0, len(rbac.Roles()))
	for _, r := range rbac.Roles() {
		names = append(names, string(r))
	}
	return strings.Join(names, ", ")
}

// UsersAdd prompts for a password (or reads GOLEM_ADMIN_PASSWORD), bcrypt-hashes it,
// and inserts a new User with the given role. The env var allows non-interactive
// use in Docker.
func UsersAdd(gdb *gorm.DB, username, role string) error {
	parsed, ok := rbac.ParseRole(role)
	if !ok {
		return fmt.Errorf("%w %q (valid: %s)", ErrUnknownRole, role, roleList())
	}
	var pass []byte
	if env := os.Getenv("GOLEM_ADMIN_PASSWORD"); env != "" {
		pass = []byte(env)
	} else {
		fmt.Printf("Password for %s: ", username)
		var err error
		pass, err = term.ReadPassword(int(os.Stdin.Fd()))
		if err != nil {
			return err
		}
		fmt.Println()
	}
	if err := ValidatePassword(string(pass)); err != nil {
		return err
	}
	hash, err := bcrypt.GenerateFromPassword(pass, bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	return gdb.Create(&db.User{Username: username, PasswordHash: string(hash), Role: string(parsed)}).Error
}

// UsersList returns all users ordered by username.
func UsersList(gdb *gorm.DB) ([]db.User, error) {
	var users []db.User
	return users, gdb.Order("username asc").Find(&users).Error
}

// UsersSetRole changes a user's role, refusing to demote the last admin.
func UsersSetRole(gdb *gorm.DB, username, role string) error {
	parsed, ok := rbac.ParseRole(role)
	if !ok {
		return fmt.Errorf("%w %q (valid: %s)", ErrUnknownRole, role, roleList())
	}
	var user db.User
	if err := gdb.Where("username = ?", username).First(&user).Error; err != nil {
		return err
	}
	if parsed == rbac.Role(user.Role) {
		return nil
	}
	if orphan, err := wouldOrphanAdmins(gdb, user); err != nil {
		return err
	} else if orphan {
		return ErrLastAdmin
	}
	return gdb.Model(&user).Update("role", string(parsed)).Error
}

// UsersRemove hard-deletes the user with the given username along with all
// their sessions, so their access is revoked immediately. It refuses to remove
// the last admin, and reports an error if the user does not exist.
func UsersRemove(gdb *gorm.DB, username string) error {
	var user db.User
	if err := gdb.Where("username = ?", username).First(&user).Error; err != nil {
		return err
	}
	if orphan, err := wouldOrphanAdmins(gdb, user); err != nil {
		return err
	} else if orphan {
		return ErrLastAdmin
	}
	if err := gdb.Where("user_id = ?", user.ID).Delete(&db.Session{}).Error; err != nil {
		return err
	}
	return gdb.Delete(&user).Error
}

// ShemsAdd generates a random 32-byte hex API key, stores its bcrypt hash, and
// prints the raw key once to stdout.
func ShemsAdd(gdb *gorm.DB, name string) error {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return err
	}
	key := hex.EncodeToString(raw)
	hash, err := bcrypt.GenerateFromPassword([]byte(key), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	if err := gdb.Create(&db.Shem{
		Name:       name,
		APIKeyHash: string(hash),
		Repos:      "[]",
		Status:     "offline",
	}).Error; err != nil {
		return err
	}
	fmt.Printf("API key for %s (shown once): %s\n", name, key)
	return nil
}

// ShemsAddOrUpdate creates a shem with the given name and API key, or updates
// its key hash if the shem already exists. Used for env-var-based auto-setup.
func ShemsAddOrUpdate(gdb *gorm.DB, name, key string) error {
	hash, err := bcrypt.GenerateFromPassword([]byte(key), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	var shem db.Shem
	err = gdb.Where("name = ?", name).First(&shem).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return gdb.Create(&db.Shem{Name: name, APIKeyHash: string(hash), Repos: "[]", Status: "offline"}).Error
	}
	if err != nil {
		return err
	}
	return gdb.Model(&shem).Update("api_key_hash", string(hash)).Error
}

// UsersAddOrUpdate creates a user with the given username, password and role,
// or updates the password hash and role if the user already exists.
func UsersAddOrUpdate(gdb *gorm.DB, username, password, role string) error {
	parsed, ok := rbac.ParseRole(role)
	if !ok {
		return fmt.Errorf("%w %q (valid: %s)", ErrUnknownRole, role, roleList())
	}
	if err := ValidatePassword(password); err != nil {
		return err
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	var user db.User
	err = gdb.Where("username = ?", username).First(&user).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return gdb.Create(&db.User{Username: username, PasswordHash: string(hash), Role: string(parsed)}).Error
	}
	if err != nil {
		return err
	}
	return gdb.Model(&user).Updates(map[string]any{
		"password_hash": string(hash),
		"role":          string(parsed),
	}).Error
}

// ShemsRemove hard-deletes the shem with the given name.
func ShemsRemove(gdb *gorm.DB, name string) error {
	return gdb.Where("name = ?", name).Delete(&db.Shem{}).Error
}

// ChangePassword replaces the given user's password hash after verifying the
// old password. Sessions are not revoked so the caller stays signed in on the
// device they used to change it; a separate "sign out everywhere" is a future
// feature. Returns ErrWrongPassword if oldPass does not match, or
// ErrWeakPassword if newPass fails ValidatePassword.
func ChangePassword(gdb *gorm.DB, userID uint, oldPass, newPass string) error {
	var user db.User
	if err := gdb.First(&user, userID).Error; err != nil {
		return err
	}
	if err := bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(oldPass)); err != nil {
		return ErrWrongPassword
	}
	if err := ValidatePassword(newPass); err != nil {
		return err
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(newPass), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	return gdb.Model(&user).Update("password_hash", string(hash)).Error
}

// ErrRepoInUse is returned when removing a GitHub repo row would orphan
// tickets that were ingested from it.
var ErrRepoInUse = errors.New("repo still has tickets")

// ReposList returns every persisted GitHub repo row, oldest first.
//
// The settings page shows the union of these rows and the remotes registered
// shems declare, so a remote can appear there without a row existing yet.
// Only rows are removable — a remote a shem declares comes back on its next
// registration, and the way to be rid of that one is to stop the shem
// declaring it.
func ReposList(gdb *gorm.DB) ([]db.GitHubRepo, error) {
	var repos []db.GitHubRepo
	err := gdb.Order("id asc").Find(&repos).Error
	return repos, err
}

// ReposRemove deletes the GitHub repo row for remote, along with any outbox
// rows queued against its tickets.
//
// Refuses while tickets from that repo exist unless force is set: those
// tickets keep their issue_number and repo_remote, and the claim predicates
// require a matching approval, so deleting the row underneath them leaves
// work that can never sync and whose provenance no longer resolves. The
// caller is told the count so the choice is an informed one.
//
// This exists because there was no way to remove a repo at all — not in the
// UI, not here — so a row saved once was permanent short of hand-written SQL
// against the database file.
func ReposRemove(gdb *gorm.DB, remote string, force bool) error {
	var repo db.GitHubRepo
	if err := gdb.Where("repo_remote = ?", remote).First(&repo).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return fmt.Errorf("no repo row for %q (a remote a shem declares has no row until you save its settings)", remote)
		}
		return err
	}

	var tickets int64
	if err := gdb.Model(&db.Ticket{}).Where("repo_remote = ?", remote).Count(&tickets).Error; err != nil {
		return err
	}
	if tickets > 0 && !force {
		return fmt.Errorf("%w: %d ticket(s) came from %s; re-run with --force to remove it anyway",
			ErrRepoInUse, tickets, remote)
	}

	return gdb.Transaction(func(tx *gorm.DB) error {
		// Outbox rows are keyed by ticket, not by repo, so they are cleared
		// via the tickets that belong to this remote. Left behind they would
		// retry forever against a repo whose settings no longer exist.
		var ticketIDs []string
		if err := tx.Model(&db.Ticket{}).Where("repo_remote = ?", remote).
			Pluck("id", &ticketIDs).Error; err != nil {
			return err
		}
		if len(ticketIDs) > 0 {
			if err := tx.Where("ticket_id IN ?", ticketIDs).
				Delete(&db.GitHubOutbox{}).Error; err != nil {
				return err
			}
		}
		return tx.Where("repo_remote = ?", remote).Delete(&db.GitHubRepo{}).Error
	})
}
