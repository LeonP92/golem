package api_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/leonp92/golem/internal/orchestrator/api"
	"github.com/leonp92/golem/internal/orchestrator/db"
	"github.com/leonp92/golem/internal/orchestrator/sse"
	ws "github.com/leonp92/golem/internal/orchestrator/ws"
)

// fakeTrigger records TriggerSync calls and returns a fixed result, standing
// in for *ghsync.Worker in these handler tests.
type fakeTrigger struct {
	calls []uint
	ret   bool
}

func (f *fakeTrigger) TriggerSync(repoID uint) bool {
	f.calls = append(f.calls, repoID)
	return f.ret
}

// setupGitHubSyncTest creates a db, handlers, and mux with only the GitHub
// routes registered — this test file doesn't need ticket/human routes.
func setupGitHubSyncTest(t *testing.T) (*api.Handlers, *http.ServeMux) {
	t.Helper()
	gdb, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	h := api.NewHandlers(gdb, ws.NewHub(), sse.NewBroker())
	mux := http.NewServeMux()
	h.RegisterGitHubRoutes(mux)
	return h, mux
}

func seedSyncRepo(t *testing.T, h *api.Handlers, enabled bool, lastManualSync *time.Time) db.GitHubRepo {
	t.Helper()
	repo := db.GitHubRepo{
		RepoRemote:     "https://github.com/org/repo",
		Owner:          "org",
		Name:           "repo",
		Enabled:        enabled,
		Label:          "golem",
		LastManualSync: lastManualSync,
	}
	if err := h.DB.Create(&repo).Error; err != nil {
		t.Fatalf("seed repo: %v", err)
	}
	return repo
}

func timePtr(t time.Time) *time.Time { return &t }

func syncURL(repoID uint) string {
	return fmt.Sprintf("/api/github/repos/%d/sync", repoID)
}

// TestManualSync covers the cooldown boundary in both directions and the
// happy path: never synced is allowed, inside the cooldown window is
// rejected with 429 and TriggerSync is never called, and once the cooldown
// has elapsed the request is allowed again. LastManualSync is stamped only
// on the accepted path.
func TestManualSync(t *testing.T) {
	tests := []struct {
		name       string
		lastSync   *time.Time
		wantStatus int
		wantCalls  int
	}{
		{
			name:       "never synced is allowed",
			lastSync:   nil,
			wantStatus: http.StatusAccepted,
			wantCalls:  1,
		},
		{
			name:       "inside cooldown returns 429",
			lastSync:   timePtr(time.Now().Add(-5 * time.Second)),
			wantStatus: http.StatusTooManyRequests,
			wantCalls:  0,
		},
		{
			name:       "after cooldown is allowed",
			lastSync:   timePtr(time.Now().Add(-5 * time.Minute)),
			wantStatus: http.StatusAccepted,
			wantCalls:  1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, mux := setupGitHubSyncTest(t)
			trig := &fakeTrigger{ret: true}
			h.Sync = trig
			h.ManualSyncCooldown = time.Minute

			_, cookie := seedSessionUser(t, h.DB, "sync-admin")
			repo := seedSyncRepo(t, h, true, tt.lastSync)

			req := httptest.NewRequest(http.MethodPost, syncURL(repo.ID), nil)
			req.AddCookie(cookie)
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)

			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d: %s", rec.Code, tt.wantStatus, rec.Body.String())
			}
			if len(trig.calls) != tt.wantCalls {
				t.Errorf("TriggerSync calls = %d, want %d", len(trig.calls), tt.wantCalls)
			}

			if tt.wantStatus == http.StatusTooManyRequests {
				if ra := rec.Header().Get("Retry-After"); ra == "" {
					t.Error("expected Retry-After header on 429")
				} else if secs, err := strconv.Atoi(ra); err != nil || secs <= 0 {
					t.Errorf("Retry-After = %q, want a positive integer", ra)
				}
			}

			var got db.GitHubRepo
			if err := h.DB.First(&got, repo.ID).Error; err != nil {
				t.Fatalf("reload repo: %v", err)
			}
			if tt.wantStatus == http.StatusAccepted {
				if got.LastManualSync == nil {
					t.Error("LastManualSync not stamped after an accepted sync")
				}
			} else {
				// A 429 must never extend (or start) the cooldown: the
				// stored value must be exactly what we seeded.
				switch {
				case tt.lastSync == nil && got.LastManualSync != nil:
					t.Error("LastManualSync stamped despite a 429")
				case tt.lastSync != nil && (got.LastManualSync == nil || !got.LastManualSync.Equal(*tt.lastSync)):
					t.Errorf("LastManualSync changed on a 429: got %v, want %v", got.LastManualSync, tt.lastSync)
				}
			}
		})
	}
}

// TestManualSyncRejectsAPIKeyCaller verifies that a shem's API key cannot
// drive manual sync: the route is guarded by auth.RequireSession, which — as
// with every other session-only JSON endpoint in this package (e.g.
// POST /api/tickets/{id}/actions) — redirects an unauthenticated caller to
// /login (302) rather than returning a JSON 401/403. The substantive
// property under test is that the shem's credentials are not honored: the
// handler must never run and TriggerSync must never be called.
func TestManualSyncRejectsAPIKeyCaller(t *testing.T) {
	h, mux := setupGitHubSyncTest(t)
	trig := &fakeTrigger{ret: true}
	h.Sync = trig
	h.ManualSyncCooldown = time.Minute

	seedShemForHuman(t, h, "sync-shem", "synckey")
	repo := seedSyncRepo(t, h, true, nil)

	req := httptest.NewRequest(http.MethodPost, syncURL(repo.ID), nil)
	req.Header.Set("Authorization", "Bearer synckey")
	req.Header.Set("X-Shem-Name", "sync-shem")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code == http.StatusAccepted {
		t.Fatal("API-key caller was allowed to trigger a manual sync")
	}
	if rec.Code != http.StatusFound {
		t.Errorf("status = %d, want %d (RequireSession's redirect-to-login)", rec.Code, http.StatusFound)
	}
	if len(trig.calls) != 0 {
		t.Errorf("TriggerSync calls = %d, want 0", len(trig.calls))
	}
}

// TestManualSync_UnauthenticatedCaller verifies a request with no
// credentials at all is rejected the same way a shem's API key is.
func TestManualSync_UnauthenticatedCaller(t *testing.T) {
	h, mux := setupGitHubSyncTest(t)
	trig := &fakeTrigger{ret: true}
	h.Sync = trig
	h.ManualSyncCooldown = time.Minute

	repo := seedSyncRepo(t, h, true, nil)

	req := httptest.NewRequest(http.MethodPost, syncURL(repo.ID), nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusFound {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusFound)
	}
	if len(trig.calls) != 0 {
		t.Errorf("TriggerSync calls = %d, want 0", len(trig.calls))
	}
}

// TestManualSync_UnknownRepoReturns404 verifies that an ID with no matching
// row is rejected before TriggerSync is ever consulted — the handler must
// resolve and authorize the repo from the database first, since
// ghsync.Worker.triggers is an unbounded map keyed by whatever repo ID
// reaches TriggerSync and entries are never released.
func TestManualSync_UnknownRepoReturns404(t *testing.T) {
	h, mux := setupGitHubSyncTest(t)
	trig := &fakeTrigger{ret: true}
	h.Sync = trig
	h.ManualSyncCooldown = time.Minute

	_, cookie := seedSessionUser(t, h.DB, "sync-admin")

	req := httptest.NewRequest(http.MethodPost, syncURL(999), nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404: %s", rec.Code, rec.Body.String())
	}
	if len(trig.calls) != 0 {
		t.Errorf("TriggerSync calls = %d, want 0", len(trig.calls))
	}
}

// TestManualSync_DisabledRepoReturns409 verifies a repo that exists but has
// sync disabled is rejected before TriggerSync is ever called.
func TestManualSync_DisabledRepoReturns409(t *testing.T) {
	h, mux := setupGitHubSyncTest(t)
	trig := &fakeTrigger{ret: true}
	h.Sync = trig
	h.ManualSyncCooldown = time.Minute

	_, cookie := seedSessionUser(t, h.DB, "sync-admin")
	repo := seedSyncRepo(t, h, false, nil)

	req := httptest.NewRequest(http.MethodPost, syncURL(repo.ID), nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusConflict {
		t.Errorf("status = %d, want 409: %s", rec.Code, rec.Body.String())
	}
	if len(trig.calls) != 0 {
		t.Errorf("TriggerSync calls = %d, want 0", len(trig.calls))
	}

	var got db.GitHubRepo
	if err := h.DB.First(&got, repo.ID).Error; err != nil {
		t.Fatalf("reload repo: %v", err)
	}
	if got.LastManualSync != nil {
		t.Error("LastManualSync stamped despite a 409")
	}
}

// TestManualSync_NilSyncReturns503 verifies that a nil h.Sync — the default
// install, where no repos are enabled or the GitHub token is missing —
// produces a clean 503 rather than a nil-pointer panic, and that
// LastManualSync is left untouched.
func TestManualSync_NilSyncReturns503(t *testing.T) {
	h, mux := setupGitHubSyncTest(t)
	h.Sync = nil
	h.ManualSyncCooldown = time.Minute

	_, cookie := seedSessionUser(t, h.DB, "sync-admin")
	repo := seedSyncRepo(t, h, true, nil)

	req := httptest.NewRequest(http.MethodPost, syncURL(repo.ID), nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503: %s", rec.Code, rec.Body.String())
	}

	var got db.GitHubRepo
	if err := h.DB.First(&got, repo.ID).Error; err != nil {
		t.Fatalf("reload repo: %v", err)
	}
	if got.LastManualSync != nil {
		t.Error("LastManualSync stamped despite a 503")
	}
}
