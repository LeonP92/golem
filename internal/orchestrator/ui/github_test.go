package ui_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/leonp92/golem/internal/orchestrator/auth"
	"github.com/leonp92/golem/internal/orchestrator/db"
	"github.com/leonp92/golem/internal/orchestrator/ui"
)

// TestGitHubSettingsPage_EscapesUserSuppliedValues guards RISK AREA 3:
// Label is user-supplied and LastError carries raw GitHub API error text, so
// both must go through html/template's normal escaping — never
// template.HTML — or a crafted label/error becomes stored XSS on this page.
func TestGitHubSettingsPage_EscapesUserSuppliedValues(t *testing.T) {
	gdb, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	repo := db.GitHubRepo{RepoRemote: "https://github.com/org/repo",
		Owner: "org", Name: "repo", Enabled: true,
		Label:     `<script>alert("label")</script>`,
		LastError: `<img src=x onerror=alert('err')>`,
	}
	if err := gdb.Create(&repo).Error; err != nil {
		t.Fatalf("seed repo: %v", err)
	}

	user := db.User{Username: "leon", PasswordHash: "x"}
	gdb.Create(&user)
	w := httptest.NewRecorder()
	if err := auth.CreateSession(gdb, w, user.ID, false); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	cookie := w.Result().Cookies()[0]

	tmpls, err := ui.LoadTemplates()
	if err != nil {
		t.Fatalf("LoadTemplates: %v", err)
	}
	h := ui.NewHandlersWithMap(gdb, tmpls, false)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	req := httptest.NewRequest(http.MethodGet, "/settings/github", nil)
	req.AddCookie(cookie)
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if strings.Contains(body, "<script>alert") {
		t.Error("Label was rendered unescaped — stored XSS")
	}
	if strings.Contains(body, "<img src=x onerror") {
		t.Error("LastError was rendered unescaped — stored XSS")
	}
	if !strings.Contains(body, "&lt;script&gt;") {
		t.Error("expected Label to appear HTML-escaped")
	}
	if !strings.Contains(body, "&lt;img") {
		t.Error("expected LastError to appear HTML-escaped")
	}
}

func TestGitHubSettingsPageListsRepos(t *testing.T) {
	gdb, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	repo := db.GitHubRepo{RepoRemote: "https://github.com/org/repo",
		Owner: "org", Name: "repo", Enabled: true, Label: "golem"}
	if err := gdb.Create(&repo).Error; err != nil {
		t.Fatalf("seed repo: %v", err)
	}

	user := db.User{Username: "leon", PasswordHash: "x"}
	gdb.Create(&user)
	w := httptest.NewRecorder()
	if err := auth.CreateSession(gdb, w, user.ID, false); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	cookie := w.Result().Cookies()[0]

	tmpls, err := ui.LoadTemplates()
	if err != nil {
		t.Fatalf("LoadTemplates: %v", err)
	}
	h := ui.NewHandlersWithMap(gdb, tmpls, false)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	req := httptest.NewRequest(http.MethodGet, "/settings/github", nil)
	req.AddCookie(cookie)
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	for _, want := range []string{"org/repo", "golem", "Sync now"} {
		if !strings.Contains(body, want) {
			t.Errorf("page missing %q", want)
		}
	}
}

func TestGitHubSettingsPage_RequiresSession(t *testing.T) {
	gdb, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}

	h := ui.NewHandlers(gdb, nil)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	req := httptest.NewRequest(http.MethodGet, "/settings/github", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusFound {
		t.Errorf("expected redirect, got %d", w.Code)
	}
	if w.Header().Get("Location") != "/login" {
		t.Errorf("expected /login redirect, got %q", w.Header().Get("Location"))
	}
}

// TestGitHubSettingsPage_PlaceholderRepoHasNoSyncButton guards RISK AREA 4:
// a repo a shem has registered but that has no db.GitHubRepo settings row
// yet must not render a "Sync now" button wired to id 0 — that endpoint
// 404s, and would leave the button stuck disabled.
func TestGitHubSettingsPage_PlaceholderRepoHasNoSyncButton(t *testing.T) {
	gdb, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	shem := db.Shem{
		Name:       "shem1",
		APIKeyHash: "x",
		Repos:      `["https://github.com/org/registered","https://github.com/org/unregistered"]`,
		Status:     "offline",
	}
	if err := gdb.Create(&shem).Error; err != nil {
		t.Fatalf("seed shem: %v", err)
	}
	registered := db.GitHubRepo{RepoRemote: "https://github.com/org/registered",
		Owner: "org", Name: "registered", Enabled: true, Label: "golem"}
	if err := gdb.Create(&registered).Error; err != nil {
		t.Fatalf("seed repo: %v", err)
	}

	user := db.User{Username: "leon", PasswordHash: "x"}
	gdb.Create(&user)
	w := httptest.NewRecorder()
	if err := auth.CreateSession(gdb, w, user.ID, false); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	cookie := w.Result().Cookies()[0]

	tmpls, err := ui.LoadTemplates()
	if err != nil {
		t.Fatalf("LoadTemplates: %v", err)
	}
	h := ui.NewHandlersWithMap(gdb, tmpls, false)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	req := httptest.NewRequest(http.MethodGet, "/settings/github", nil)
	req.AddCookie(cookie)
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !strings.Contains(body, "org/unregistered") {
		t.Error("expected the shem-registered-but-unsaved repo to be listed")
	}
	if strings.Contains(body, `data-repo-id="0"`) {
		t.Error("placeholder repo (no settings row) must not offer a sync button wired to id 0")
	}
}

func TestGitHubSettingsSubmit(t *testing.T) {
	cases := []struct {
		name        string
		seed        *db.GitHubRepo
		remote      string
		formBody    string
		wantEnabled bool
		wantLabel   string
	}{
		{
			name:        "creates a new row when none exists",
			remote:      "https://github.com/org/new",
			formBody:    "repo_remote=https://github.com/org/new&enabled=on&label=custom",
			wantEnabled: true,
			wantLabel:   "custom",
		},
		{
			name: "disables an existing row when the checkbox is absent",
			seed: &db.GitHubRepo{RepoRemote: "https://github.com/org/existing",
				Owner: "org", Name: "existing", Enabled: true, Label: "golem"},
			remote:      "https://github.com/org/existing",
			formBody:    "repo_remote=https://github.com/org/existing&label=golem",
			wantEnabled: false,
			wantLabel:   "golem",
		},
		{
			name: "updates the label on an existing row",
			seed: &db.GitHubRepo{RepoRemote: "https://github.com/org/relabel",
				Owner: "org", Name: "relabel", Enabled: true, Label: "golem"},
			remote:      "https://github.com/org/relabel",
			formBody:    "repo_remote=https://github.com/org/relabel&enabled=on&label=renamed",
			wantEnabled: true,
			wantLabel:   "renamed",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gdb, err := db.Open(":memory:")
			if err != nil {
				t.Fatalf("db.Open: %v", err)
			}
			if tc.seed != nil {
				if err := gdb.Create(tc.seed).Error; err != nil {
					t.Fatalf("seed: %v", err)
				}
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

			req := httptest.NewRequest(http.MethodPost, "/settings/github", strings.NewReader(tc.formBody))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			req.AddCookie(cookie)
			w = httptest.NewRecorder()
			mux.ServeHTTP(w, req)

			if w.Code != http.StatusSeeOther {
				t.Fatalf("status = %d, want 303: %s", w.Code, w.Body.String())
			}
			if loc := w.Header().Get("Location"); loc != "/settings/github" {
				t.Errorf("Location = %q, want /settings/github", loc)
			}

			var repo db.GitHubRepo
			if err := gdb.Where("repo_remote = ?", tc.remote).First(&repo).Error; err != nil {
				t.Fatalf("expected a row for %s: %v", tc.remote, err)
			}
			if repo.Enabled != tc.wantEnabled {
				t.Errorf("Enabled = %v, want %v", repo.Enabled, tc.wantEnabled)
			}
			if repo.Label != tc.wantLabel {
				t.Errorf("Label = %q, want %q", repo.Label, tc.wantLabel)
			}
		})
	}
}

func TestTicketDetailShowsIssueLinkAndReadOnlyTitle(t *testing.T) {
	gdb, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	n := 7
	ticket := db.Ticket{ID: "gh-issue-001", RepoRemote: "https://github.com/org/repo",
		Title: "Add rate limiting", Branch: "ticket/x-t1", Description: "d",
		Phase: "implement", IssueNumber: &n,
		IssueURL: "https://github.com/org/repo/issues/7"}
	if err := gdb.Create(&ticket).Error; err != nil {
		t.Fatalf("seed ticket: %v", err)
	}

	user := db.User{Username: "leon", PasswordHash: "x"}
	gdb.Create(&user)
	w := httptest.NewRecorder()
	if err := auth.CreateSession(gdb, w, user.ID, false); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	cookie := w.Result().Cookies()[0]

	tmpls, err := ui.LoadTemplates()
	if err != nil {
		t.Fatalf("LoadTemplates: %v", err)
	}
	h := ui.NewHandlersWithMap(gdb, tmpls, false)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	req := httptest.NewRequest(http.MethodGet, "/tickets/gh-issue-001", nil)
	req.AddCookie(cookie)
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !strings.Contains(body, "https://github.com/org/repo/issues/7") {
		t.Error("issue URL not rendered")
	}
	if !strings.Contains(body, "synced from GitHub") {
		t.Error("read-only provenance note missing — an editable title would silently revert on the next ingest")
	}
}

// TestTicketDetailNonLinkedTicketUnchanged guards against regressing the
// non-GitHub-linked ticket rendering path: it must show no GitHub
// provenance note, since .Ticket.IssueNumber is nil.
func TestTicketDetailNonLinkedTicketUnchanged(t *testing.T) {
	gdb, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	ticket := db.Ticket{ID: "gh-issue-002", RepoRemote: "https://github.com/org/repo",
		Title: "Plain ticket", Branch: "ticket/x-t2", Description: "d",
		Phase: "implement"}
	if err := gdb.Create(&ticket).Error; err != nil {
		t.Fatalf("seed ticket: %v", err)
	}

	user := db.User{Username: "leon", PasswordHash: "x"}
	gdb.Create(&user)
	w := httptest.NewRecorder()
	if err := auth.CreateSession(gdb, w, user.ID, false); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	cookie := w.Result().Cookies()[0]

	tmpls, err := ui.LoadTemplates()
	if err != nil {
		t.Fatalf("LoadTemplates: %v", err)
	}
	h := ui.NewHandlersWithMap(gdb, tmpls, false)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	req := httptest.NewRequest(http.MethodGet, "/tickets/gh-issue-002", nil)
	req.AddCookie(cookie)
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if strings.Contains(body, "synced from GitHub") {
		t.Error("non-linked ticket must not show a GitHub provenance note")
	}
}

// TestDashboardShowsIssueBadgeForLinkedTicket guards the ticket_row partial:
// its dot context is ui.TicketRow (with a nested .Ticket), so the badge must
// reference .Ticket.IssueNumber/.Ticket.IssueURL, not a bare .IssueNumber —
// the latter would fail template execution for every ticket on the
// dashboard, not just the linked one.
func TestDashboardShowsIssueBadgeForLinkedTicket(t *testing.T) {
	gdb, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	n := 42
	ticket := db.Ticket{ID: "gh-issue-003", RepoRemote: "https://github.com/org/repo",
		Title: "Linked", Branch: "ticket/x-t3", Description: "d",
		Phase: "unassigned", IssueNumber: &n,
		IssueURL: "https://github.com/org/repo/issues/42"}
	if err := gdb.Create(&ticket).Error; err != nil {
		t.Fatalf("seed ticket: %v", err)
	}

	user := db.User{Username: "leon", PasswordHash: "x"}
	gdb.Create(&user)
	w := httptest.NewRecorder()
	if err := auth.CreateSession(gdb, w, user.ID, false); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	cookie := w.Result().Cookies()[0]

	tmpls, err := ui.LoadTemplates()
	if err != nil {
		t.Fatalf("LoadTemplates: %v", err)
	}
	h := ui.NewHandlersWithMap(gdb, tmpls, false)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	req := httptest.NewRequest(http.MethodGet, "/dashboard", nil)
	req.AddCookie(cookie)
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !strings.Contains(body, `href="https://github.com/org/repo/issues/42"`) {
		t.Error("expected issue badge link on dashboard row")
	}
	if !strings.Contains(body, "#42") {
		t.Error("expected issue number on dashboard row")
	}
}
