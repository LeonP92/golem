package auth_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/leonp92/golem/internal/orchestrator/auth"
	"github.com/leonp92/golem/internal/orchestrator/db"
)

func TestSessionRoundTrip(t *testing.T) {
	gdb, _ := db.Open(":memory:")
	user := db.User{Username: "alice", PasswordHash: "x"}
	gdb.Create(&user)

	w := httptest.NewRecorder()
	if err := auth.CreateSession(gdb, w, user.ID, false); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	cookie := w.Result().Cookies()[0]

	req := httptest.NewRequest("GET", "/", nil)
	req.AddCookie(cookie)

	called := false
	handler := auth.RequireSession(gdb)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		u := auth.SessionUser(r)
		if u == nil || u.Username != "alice" {
			t.Error("expected alice in context")
		}
	}))
	handler.ServeHTTP(httptest.NewRecorder(), req)
	if !called {
		t.Error("handler not called")
	}
}

func TestRequireSession_RedirectsWithoutCookie(t *testing.T) {
	gdb, _ := db.Open(":memory:")
	req := httptest.NewRequest("GET", "/dashboard", nil)
	w := httptest.NewRecorder()
	auth.RequireSession(gdb)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("should not reach handler")
	})).ServeHTTP(w, req)
	if w.Code != http.StatusFound {
		t.Errorf("expected 302, got %d", w.Code)
	}
}
