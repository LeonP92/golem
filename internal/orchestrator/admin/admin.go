package admin

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"

	"golang.org/x/crypto/bcrypt"
	"golang.org/x/term"

	"github.com/leonp92/golem/internal/orchestrator/db"
	"gorm.io/gorm"
)

// UsersAdd prompts for a password (or reads GOLEM_ADMIN_PASSWORD), bcrypt-hashes it,
// and inserts a new User. The env var allows non-interactive use in Docker.
func UsersAdd(gdb *gorm.DB, username string) error {
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
	return gdb.Create(&db.User{Username: username, PasswordHash: string(hash)}).Error
}

// UsersRemove hard-deletes the user with the given username.
func UsersRemove(gdb *gorm.DB, username string) error {
	return gdb.Where("username = ?", username).Delete(&db.User{}).Error
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
	result := gdb.Where("name = ?", name).FirstOrCreate(&shem, db.Shem{
		Name:   name,
		Repos:  "[]",
		Status: "offline",
	})
	if result.Error != nil {
		return result.Error
	}
	return gdb.Model(&shem).Update("api_key_hash", string(hash)).Error
}

// UsersAddOrUpdate creates a user with the given username and password, or
// updates the password hash if the user already exists.
func UsersAddOrUpdate(gdb *gorm.DB, username, password string) error {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	var user db.User
	result := gdb.Where("username = ?", username).FirstOrCreate(&user, db.User{
		Username:     username,
		PasswordHash: string(hash),
	})
	if result.Error != nil {
		return result.Error
	}
	return gdb.Model(&user).Update("password_hash", string(hash)).Error
}

// ShemsRemove hard-deletes the shem with the given name.
func ShemsRemove(gdb *gorm.DB, name string) error {
	return gdb.Where("name = ?", name).Delete(&db.Shem{}).Error
}
