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

// A repository whose remote is not on github.com must not be storable as an
// enabled, syncable repo.
//
// Owner and Name go straight into GitHub API paths. Before the host check,
// saving a remote on a look-alike host stored a perfectly ordinary-looking
// row — owner "acme", name "widgets" — and the sync loop then read issues
// from and posted comments to github.com/acme/widgets, a repository the
// operator never named. Nothing on the page showed the substitution, because
// as far as the row was concerned nothing was wrong.
func TestGitHubSettings_RejectsNonGitHubRemote(t *testing.T) {
	for _, remote := range []string{
		"https://github.com.evil.example/acme/widgets",
		"https://evil.example/acme/widgets",
		"https://gitlab.com/acme/widgets",
		"https://github.com/acme",
	} {
		t.Run(remote, func(t *testing.T) {
			gdb, err := db.Open(":memory:")
			if err != nil {
				t.Fatalf("db.Open: %v", err)
			}
			user := db.User{Username: "leon", PasswordHash: "x", Role: string(rbac.RoleAdmin)}
			if err := gdb.Create(&user).Error; err != nil {
				t.Fatalf("create user: %v", err)
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
			mux := http.NewServeMux()
			ui.NewHandlersWithMap(gdb, tmpls, false).RegisterRoutes(mux)

			form := "repo_remote=" + remote + "&enabled=on"
			post := httptest.NewRequest(http.MethodPost, "/settings/github", strings.NewReader(form))
			post.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			post.AddCookie(cookie)
			post.Header.Set(auth.CSRFHeader, auth.CSRFTokenForSession(cookie.Value))
			pw := httptest.NewRecorder()
			mux.ServeHTTP(pw, post)

			if pw.Code == http.StatusSeeOther {
				t.Errorf("saving %q succeeded; a remote that is not a github.com "+
					"repository must be refused, not stored", remote)
			}

			var count int64
			gdb.Model(&db.GitHubRepo{}).Count(&count)
			if count != 0 {
				t.Errorf("saving %q stored a GitHubRepo row; its owner/name would be "+
					"used against github.com", remote)
			}
		})
	}
}

// The canonical case must still work, or the check has simply broken the
// feature.
func TestGitHubSettings_AcceptsGitHubRemote(t *testing.T) {
	gdb, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	user := db.User{Username: "leon", PasswordHash: "x", Role: string(rbac.RoleAdmin)}
	if err := gdb.Create(&user).Error; err != nil {
		t.Fatalf("create user: %v", err)
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
	mux := http.NewServeMux()
	ui.NewHandlersWithMap(gdb, tmpls, false).RegisterRoutes(mux)

	form := "repo_remote=https://github.com/acme/widgets&enabled=on"
	post := httptest.NewRequest(http.MethodPost, "/settings/github", strings.NewReader(form))
	post.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	post.AddCookie(cookie)
	post.Header.Set(auth.CSRFHeader, auth.CSRFTokenForSession(cookie.Value))
	pw := httptest.NewRecorder()
	mux.ServeHTTP(pw, post)
	if pw.Code != http.StatusSeeOther {
		t.Fatalf("POST a github.com remote = %d: %s", pw.Code, pw.Body.String())
	}
	var saved db.GitHubRepo
	if err := gdb.Where("repo_remote = ?", "https://github.com/acme/widgets").First(&saved).Error; err != nil {
		t.Fatalf("load saved repo: %v", err)
	}
	if saved.Owner != "acme" || saved.Name != "widgets" {
		t.Errorf("saved owner/name = %q/%q, want acme/widgets", saved.Owner, saved.Name)
	}
}
