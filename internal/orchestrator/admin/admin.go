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
