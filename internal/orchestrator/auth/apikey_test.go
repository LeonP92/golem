package auth_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"golang.org/x/crypto/bcrypt"

	"github.com/leonp92/golem/internal/orchestrator/auth"
	"github.com/leonp92/golem/internal/orchestrator/db"
)

func TestAPIKeyAuth(t *testing.T) {
	gdb, _ := db.Open(":memory:")
	hash, _ := bcrypt.GenerateFromPassword([]byte("secret"), bcrypt.MinCost)
	shem := db.Shem{Name: "node-a", APIKeyHash: string(hash), Repos: "[]", Status: "online"}
	gdb.Create(&shem)

	req := httptest.NewRequest("POST", "/api/shems/register", nil)
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("X-Shem-Name", "node-a")
	w := httptest.NewRecorder()

	called := false
	auth.RequireAPIKey(gdb)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		s := auth.ShemFromRequest(r)
		if s == nil || s.Name != "node-a" {
			t.Error("expected node-a shem in context")
		}
	})).ServeHTTP(w, req)
	if !called {
		t.Error("handler not called")
	}
}

func TestAPIKeyAuth_Unauthorized(t *testing.T) {
	gdb, _ := db.Open(":memory:")
	hash, _ := bcrypt.GenerateFromPassword([]byte("secret"), bcrypt.MinCost)
	shem := db.Shem{Name: "node-b", APIKeyHash: string(hash), Repos: "[]", Status: "online"}
	gdb.Create(&shem)

	req := httptest.NewRequest("POST", "/api/shems/register", nil)
	req.Header.Set("Authorization", "Bearer wrongkey")
	req.Header.Set("X-Shem-Name", "node-b")
	w := httptest.NewRecorder()

	auth.RequireAPIKey(gdb)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("should not reach handler")
	})).ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", w.Code)
	}
}

func TestAPIKeyAuth_MissingName(t *testing.T) {
	gdb, _ := db.Open(":memory:")

	req := httptest.NewRequest("POST", "/api/shems/register", nil)
	req.Header.Set("Authorization", "Bearer secret")
	// No X-Shem-Name header
	w := httptest.NewRecorder()

	auth.RequireAPIKey(gdb)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("should not reach handler")
	})).ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", w.Code)
	}
}
