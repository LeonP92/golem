package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"time"

	"github.com/leonp92/golem/internal/orchestrator/db"
	"gorm.io/gorm"
)

type contextKey string

const sessionUserKey contextKey = "session_user"

const sessionTTL = 7 * 24 * time.Hour
const cookieName = "golem_session"

// CreateSession generates a random token, stores its SHA-256 hash in db.Session,
// sets an HTTP-only cookie named "golem_session", 7-day TTL.
// secure should be true when the server is running behind TLS.
func CreateSession(gdb *gorm.DB, w http.ResponseWriter, userID uint, secure bool) error {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return err
	}
	token := hex.EncodeToString(raw)
	hash := sha256.Sum256([]byte(token))
	session := db.Session{
		UserID:    userID,
		TokenHash: hex.EncodeToString(hash[:]),
		ExpiresAt: time.Now().Add(sessionTTL),
	}
	if err := gdb.Create(&session).Error; err != nil {
		return err
	}
	http.SetCookie(w, &http.Cookie{
		Name:     cookieName,
		Value:    token,
		Path:     "/",
		Expires:  session.ExpiresAt,
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
	})
	return nil
}

// RequireSession middleware: reads "golem_session" cookie, SHA-256 hashes it,
// looks up db.Session (checking expires_at > now), loads db.User,
// stores *db.User in request context via sessionUserKey.
// Redirects to /login on any failure.
//
// It also stores this session's CSRF token (see CSRFTokenForSession) in the
// context, which is where both RequireCSRF and the templates read it from.
// Doing it here rather than in RequireCSRF means a GET that only renders the
// token still gets one without being wrapped in the enforcing middleware.
func RequireSession(gdb *gorm.DB) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			cookie, err := r.Cookie(cookieName)
			if err != nil {
				http.Redirect(w, r, "/login", http.StatusFound)
				return
			}
			hash := sha256.Sum256([]byte(cookie.Value))
			var session db.Session
			if err := gdb.Where("token_hash = ? AND expires_at > ?",
				hex.EncodeToString(hash[:]), time.Now()).
				First(&session).Error; err != nil {
				http.Redirect(w, r, "/login", http.StatusFound)
				return
			}
			var user db.User
			if err := gdb.First(&user, session.UserID).Error; err != nil {
				http.Redirect(w, r, "/login", http.StatusFound)
				return
			}
			ctx := context.WithValue(r.Context(), sessionUserKey, &user)
			// Derived from the raw cookie value, which is HttpOnly and
			// therefore unreadable by page content — see CSRFTokenForSession.
			ctx = context.WithValue(ctx, csrfTokenKey, CSRFTokenForSession(cookie.Value))
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// SessionUser extracts *db.User from context (set by RequireSession).
// Returns nil if not present.
func SessionUser(r *http.Request) *db.User {
	u, _ := r.Context().Value(sessionUserKey).(*db.User)
	return u
}
