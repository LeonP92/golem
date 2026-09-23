package ui_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/leonp92/golem/internal/orchestrator/auth"
	"github.com/leonp92/golem/internal/orchestrator/db"
	"github.com/leonp92/golem/internal/orchestrator/rbac"
	"github.com/leonp92/golem/internal/orchestrator/ui"
)

// The configured default has to reach both places the literal "golem" used to
// sit, or an operator sets GOLEM_GITHUB_LABEL and still finds "golem" waiting
// in the form.
func TestGitHubDefaultLabelIsUsedForUnregisteredAndFirstSave(t *testing.T) {
	gdb, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	user := db.User{Username: "leon", PasswordHash: "x", Role: string(rbac.RoleAdmin)}
	if err := gdb.Create(&user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	// A shem declaring a repo is what makes it appear without a row.
	if err := gdb.Create(&db.Shem{
		Name: "s1", APIKeyHash: "h", Repos: `["https://github.com/acme/widgets"]`,
	}).Error; err != nil {
		t.Fatalf("create shem: %v", err)
	}
	rec := httptest.NewRecorder()
	if err := auth.CreateSession(gdb, rec, user.ID, false); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	cookie := rec.Result().Cookies()[0]

	tmpls, err := ui.LoadTemplates()
	if err != nil {
		t.Fatalf("LoadTemplates: %v", err)
	}
	h := ui.NewHandlersWithMap(gdb, tmpls, false)
	h.GitHubDefaultLabel = "needs-golem"
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	// 1. The unregistered placeholder offers the configured label.
	req := httptest.NewRequest(http.MethodGet, "/settings/github", nil)
	req.AddCookie(cookie)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("GET /settings/github = %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `value="needs-golem"`) {
		t.Error("the settings page offered a label other than the configured default")
	}

	// 2. Enabling it persists the configured label, not "golem".
	form := "repo_remote=https://github.com/acme/widgets&enabled=on"
	post := httptest.NewRequest(http.MethodPost, "/settings/github", strings.NewReader(form))
	post.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	post.AddCookie(cookie)
	post.Header.Set(auth.CSRFHeader, auth.CSRFTokenForSession(cookie.Value))
	pw := httptest.NewRecorder()
	mux.ServeHTTP(pw, post)
	if pw.Code != http.StatusSeeOther {
		t.Fatalf("POST /settings/github = %d: %s", pw.Code, pw.Body.String())
	}
	var saved db.GitHubRepo
	if err := gdb.Where("repo_remote = ?", "https://github.com/acme/widgets").
		First(&saved).Error; err != nil {
		t.Fatalf("load saved repo: %v", err)
	}
	if saved.Label != "needs-golem" {
		t.Errorf("saved label = %q, want the configured default", saved.Label)
	}
}
