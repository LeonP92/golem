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

// RequireAPIKey middleware: reads "Authorization: Bearer <key>" header,
// iterates all db.Shem rows (bcrypt.CompareHashAndPassword against APIKeyHash),
// stores *db.Shem in request context via shemKey.
// Returns 401 on failure.
func RequireAPIKey(gdb *gorm.DB) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			hdr := r.Header.Get("Authorization")
			key, ok := strings.CutPrefix(hdr, "Bearer ")
			if !ok || key == "" {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			var shems []db.Shem
			if err := gdb.Find(&shems).Error; err != nil {
				http.Error(w, "internal error", http.StatusInternalServerError)
				return
			}
			for i := range shems {
				if bcrypt.CompareHashAndPassword([]byte(shems[i].APIKeyHash), []byte(key)) == nil {
					ctx := context.WithValue(r.Context(), shemKey, &shems[i])
					next.ServeHTTP(w, r.WithContext(ctx))
					return
				}
			}
			http.Error(w, "unauthorized", http.StatusUnauthorized)
		})
	}
}

// ShemFromRequest extracts *db.Shem from context (set by RequireAPIKey).
// Returns nil if not present.
func ShemFromRequest(r *http.Request) *db.Shem {
	s, _ := r.Context().Value(shemKey).(*db.Shem)
	return s
}
