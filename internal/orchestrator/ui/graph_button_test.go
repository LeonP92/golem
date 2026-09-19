package ui_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/leonp92/golem/internal/orchestrator/auth"
	"github.com/leonp92/golem/internal/orchestrator/db"
	"github.com/leonp92/golem/internal/orchestrator/rbac"
	"github.com/leonp92/golem/internal/orchestrator/ui"
	"github.com/leonp92/golem/internal/orchestrator/ws"
	"gorm.io/gorm"
)

const graphRemote = "https://github.com/acme/widgets"

func newGraphEnv(t *testing.T) (*http.ServeMux, *gorm.DB, *http.Cookie) {
	t.Helper()
	gdb, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	user := db.User{Username: "leon", PasswordHash: "x", Role: string(rbac.RoleAdmin)}
	if err := gdb.Create(&user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	if err := gdb.Create(&db.GitHubRepo{
		RepoRemote: graphRemote, Owner: "acme", Name: "widgets", Label: "golem", Enabled: true,
	}).Error; err != nil {
		t.Fatalf("seed repo: %v", err)
	}
	rec := httptest.NewRecorder()
	if err := auth.CreateSession(gdb, rec, user.ID, false); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	tmpls, err := ui.LoadTemplates()
	if err != nil {
		t.Fatalf("LoadTemplates: %v", err)
	}
	h := ui.NewHandlersWithMap(gdb, tmpls, false)
	h.Hub = ws.NewHub()
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	return mux, gdb, rec.Result().Cookies()[0]
}

func postBuild(t *testing.T, mux *http.ServeMux, cookie *http.Cookie, remote string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/settings/github/graph-build",
		strings.NewReader("repo_remote="+remote))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(cookie)
	req.Header.Set(auth.CSRFHeader, auth.CSRFTokenForSession(cookie.Value))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	return w
}

// The button exists so a graph build does not need a `docker compose exec`.
// What it must not do is claim to have started something it did not, or leave
// a row reading "building…" that nothing will ever finish.
func TestGraphBuildButton(t *testing.T) {
	t.Run("starts a build and records that it is running", func(t *testing.T) {
		mux, gdb, cookie := newGraphEnv(t)
		w := postBuild(t, mux, cookie, graphRemote)
		if w.Code != http.StatusSeeOther {
			t.Fatalf("POST = %d, want 303: %s", w.Code, w.Body.String())
		}
		var repo db.GitHubRepo
		if err := gdb.Where("repo_remote = ?", graphRemote).First(&repo).Error; err != nil {
			t.Fatalf("reload: %v", err)
		}
		if !repo.GraphBuildRunning(time.Now().UTC()) {
			t.Error("the repo does not read as building after the button was pressed")
		}
	})

	t.Run("the page shows the state and offers the button", func(t *testing.T) {
		mux, _, cookie := newGraphEnv(t)
		get := func() string {
			req := httptest.NewRequest(http.MethodGet, "/settings/github", nil)
			req.AddCookie(cookie)
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, req)
			if w.Code != http.StatusOK {
				t.Fatalf("GET = %d: %s", w.Code, w.Body.String())
			}
			return w.Body.String()
		}
		before := get()
		if !strings.Contains(before, "never built") {
			t.Error("a repo with no build does not say so")
		}
		if !strings.Contains(before, `action="/settings/github/graph-build"`) {
			t.Error("the build button is not offered")
		}
		postBuild(t, mux, cookie, graphRemote)
		if after := get(); !strings.Contains(after, "building…") {
			t.Error("a running build is not shown as running")
		}
	})

	t.Run("a second press while running is reported, not queued twice", func(t *testing.T) {
		mux, _, cookie := newGraphEnv(t)
		postBuild(t, mux, cookie, graphRemote)
		w := postBuild(t, mux, cookie, graphRemote)
		if !strings.Contains(w.Body.String(), "already running") {
			t.Errorf("a double press was not reported: %d %s", w.Code, w.Body.String())
		}
	})

	t.Run("an unregistered repo is refused with an explanation", func(t *testing.T) {
		mux, _, cookie := newGraphEnv(t)
		w := postBuild(t, mux, cookie, "https://github.com/acme/never-registered")
		if !strings.Contains(w.Body.String(), "Enable that repository") {
			t.Errorf("no explanation for an unregistered repo: %d %s", w.Code, w.Body.String())
		}
	})

	t.Run("with no hub the build is failed rather than left running", func(t *testing.T) {
		// Otherwise the row reads "building…" for the whole timeout while
		// nothing is going to pick it up.
		_, gdb, cookie := newGraphEnv(t)
		// Rebuilt with Hub left nil.
		tmpls, err := ui.LoadTemplates()
		if err != nil {
			t.Fatalf("LoadTemplates: %v", err)
		}
		h := ui.NewHandlersWithMap(gdb, tmpls, false) // Hub left nil
		mux := http.NewServeMux()
		h.RegisterRoutes(mux)

		w := postBuild(t, mux, cookie, graphRemote)
		if !strings.Contains(w.Body.String(), "No shem connection") {
			t.Errorf("no explanation when there is no hub: %d %s", w.Code, w.Body.String())
		}
		var after db.GitHubRepo
		if err := gdb.Where("repo_remote = ?", graphRemote).First(&after).Error; err != nil {
			t.Fatalf("reload: %v", err)
		}
		if after.GraphBuildRunning(time.Now().UTC()) {
			t.Error("the row still reads as building though nothing can finish it")
		}
		if after.GraphBuildError == "" {
			t.Error("no reason was recorded for the failure")
		}
	})
}
