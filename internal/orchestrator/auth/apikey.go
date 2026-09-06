package auth

import (
	"context"
	"net/http"
	"strings"

	"golang.org/x/crypto/bcrypt"

	"github.com/leonp92/golem/internal/orchestrator/db"
	"gorm.io/gorm"
)

const shemKey contextKey = "shem"

// RequireAPIKey middleware: reads "X-Shem-Name" and "Authorization: Bearer <key>"
// headers, looks up the shem by name, verifies the key, and stores *db.Shem in
// the request context. Returns 401 on any failure.
func RequireAPIKey(gdb *gorm.DB) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			name := r.Header.Get("X-Shem-Name")
			if name == "" {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			key, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
			if !ok || key == "" {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			var shem db.Shem
			if err := gdb.Where("name = ?", name).First(&shem).Error; err != nil {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			if bcrypt.CompareHashAndPassword([]byte(shem.APIKeyHash), []byte(key)) != nil {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			ctx := context.WithValue(r.Context(), shemKey, &shem)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// ShemFromRequest extracts *db.Shem from context (set by RequireAPIKey).
// Returns nil if not present.
func ShemFromRequest(r *http.Request) *db.Shem {
	s, _ := r.Context().Value(shemKey).(*db.Shem)
	return s
}
