package ui_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"golang.org/x/crypto/bcrypt"

	"github.com/leonp92/golem/internal/orchestrator/auth"
	"github.com/leonp92/golem/internal/orchestrator/db"
	"github.com/leonp92/golem/internal/orchestrator/ui"
)

func TestLoginHandler_ValidCredentials(t *testing.T) {
	gdb, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	hash, err := bcrypt.GenerateFromPassword([]byte("pass"), bcrypt.DefaultCost)
	if err != nil {
		t.Fatalf("bcrypt: %v", err)
	}
	gdb.Create(&db.User{Username: "admin", PasswordHash: string(hash)})

	h := ui.NewHandlers(gdb, nil)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	body := strings.NewReader("username=admin&password=pass")
	req := httptest.NewRequest("POST", "/login", body)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusFound {
		t.Errorf("expected redirect (302), got %d", w.Code)
	}
	if w.Header().Get("Location") != "/dashboard" {
		t.Errorf("expected /dashboard redirect, got %q", w.Header().Get("Location"))
	}
}

func TestLoginHandler_InvalidCredentials(t *testing.T) {
	gdb, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	hash, _ := bcrypt.GenerateFromPassword([]byte("correct"), bcrypt.DefaultCost)
	gdb.Create(&db.User{Username: "admin", PasswordHash: string(hash)})

	h := ui.NewHandlers(gdb, nil)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	body := strings.NewReader("username=admin&password=wrong")
	req := httptest.NewRequest("POST", "/login", body)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusFound {
		t.Errorf("expected redirect, got %d", w.Code)
	}
	loc := w.Header().Get("Location")
	if !strings.Contains(loc, "/login") {
		t.Errorf("expected redirect back to /login, got %q", loc)
	}
}

func TestLoginHandler_UnknownUser(t *testing.T) {
	gdb, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}

	h := ui.NewHandlers(gdb, nil)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	body := strings.NewReader("username=nobody&password=pass")
	req := httptest.NewRequest("POST", "/login", body)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusFound {
		t.Errorf("expected redirect, got %d", w.Code)
	}
	loc := w.Header().Get("Location")
	if !strings.Contains(loc, "/login") {
		t.Errorf("expected redirect back to /login, got %q", loc)
	}
}

func TestDashboard_RequiresSession(t *testing.T) {
	gdb, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}

	h := ui.NewHandlers(gdb, nil)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	req := httptest.NewRequest("GET", "/dashboard", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	// No session cookie → redirect to /login.
	if w.Code != http.StatusFound {
		t.Errorf("expected redirect, got %d", w.Code)
	}
	if w.Header().Get("Location") != "/login" {
		t.Errorf("expected /login redirect, got %q", w.Header().Get("Location"))
	}
}

func TestTicketNewSubmit_SetsCreatedByFromSession(t *testing.T) {
	gdb, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	user := db.User{Username: "leon", PasswordHash: "x"}
	gdb.Create(&user)
	w := httptest.NewRecorder()
	if err := auth.CreateSession(gdb, w, user.ID, false); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	cookie := w.Result().Cookies()[0]

	h := ui.NewHandlers(gdb, nil)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	body := strings.NewReader("repo_remote=https://github.com/org/repo&base_branch=main&description=test+ticket")
	req := httptest.NewRequest("POST", "/tickets/new", body)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(cookie)
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusFound {
		t.Fatalf("expected redirect (302), got %d: %s", w.Code, w.Body.String())
	}

	var ticket db.Ticket
	if err := gdb.First(&ticket).Error; err != nil {
		t.Fatalf("expected a created ticket: %v", err)
	}
	if ticket.CreatedByUserID == nil || *ticket.CreatedByUserID != user.ID {
		t.Errorf("expected CreatedByUserID=%d, got %v", user.ID, ticket.CreatedByUserID)
	}
}

func TestLoadTemplates(t *testing.T) {
	tmpls, err := ui.LoadTemplates()
	if err != nil {
		t.Fatalf("LoadTemplates: %v", err)
	}
	expected := []string{"login", "dashboard", "shems", "ticket_new", "ticket_detail"}
	for _, name := range expected {
		if tmpls[name] == nil {
			t.Errorf("missing template set for page %q", name)
		}
	}
}
