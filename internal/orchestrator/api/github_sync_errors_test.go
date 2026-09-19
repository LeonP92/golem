package api_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/leonp92/golem/internal/orchestrator/rbac"
)

// TestManualSyncFailuresAreJSON pins every refusal manualSync can produce to
// the one shape its only caller can read.
//
// The Sync now button is a hand-written fetch() that guards on content-type
// before calling r.json(). Every failure the server answered in text/plain
// therefore fell into the same catch and rendered the same "Failed — retry?",
// so a 503 (sync not running), a 404 (repo removed) and a 409 (sync disabled
// for this repo) were indistinguishable — and the two a first-time operator
// actually hits were the two with no usable message.
//
// The status codes are unchanged; only the body is. Each error string must
// also be distinct, because identical text would collapse the cases again at
// the only place the operator can see them.
func TestManualSyncFailuresAreJSON(t *testing.T) {
	tests := []struct {
		name       string
		repoID     uint
		enabled    bool
		seedRepo   bool
		syncWorker bool
		wantStatus int
		wantErrHas string
	}{
		{
			name:       "unknown repo",
			repoID:     999,
			seedRepo:   false,
			syncWorker: true,
			wantStatus: http.StatusNotFound,
			wantErrHas: "not found",
		},
		{
			name:       "sync disabled for this repo",
			enabled:    false,
			seedRepo:   true,
			syncWorker: true,
			wantStatus: http.StatusConflict,
			wantErrHas: "not enabled",
		},
		{
			name:       "sync worker not running",
			enabled:    true,
			seedRepo:   true,
			syncWorker: false,
			wantStatus: http.StatusServiceUnavailable,
			wantErrHas: "GOLEM_GITHUB_TOKEN",
		},
	}

	seen := map[string]string{}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h, mux := setupGitHubSyncTest(t)
			if tc.syncWorker {
				h.Sync = &fakeTrigger{ret: true}
			}
			h.ManualSyncCooldown = time.Minute

			_, cookie := seedSessionUser(t, h.DB, "sync-admin", string(rbac.RoleAdmin))
			id := tc.repoID
			if tc.seedRepo {
				id = seedSyncRepo(t, h, tc.enabled, nil).ID
			}

			req := httptest.NewRequest(http.MethodPost, syncURL(id), nil)
			withSession(req, cookie)
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)

			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d: %s", rec.Code, tc.wantStatus, rec.Body.String())
			}
			if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "application/json") {
				t.Fatalf("Content-Type = %q, want application/json — the page's "+
					"content-type guard turns anything else into a bare \"Failed — retry?\"", ct)
			}
			var body struct {
				Error string `json:"error"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("decode body %q: %v", rec.Body.String(), err)
			}
			if body.Error == "" {
				t.Fatal("body has no \"error\" field; the button has nothing to show")
			}
			if prev, ok := seen[body.Error]; ok {
				t.Fatalf("error %q is shared with the %q case; the operator cannot tell them apart",
					body.Error, prev)
			}
			seen[body.Error] = tc.name
		})
	}
}

// TestManualSyncNamesTheConfiguredTokenVariable: github.token_env is
// configurable, so a deployment that renamed it must not be told to set
// GOLEM_GITHUB_TOKEN, which nothing would read.
func TestManualSyncNamesTheConfiguredTokenVariable(t *testing.T) {
	tests := []struct {
		name     string
		tokenEnv string
		want     string
	}{
		{name: "renamed variable is named", tokenEnv: "ACME_GH_PAT", want: "ACME_GH_PAT"},
		{name: "unwired falls back to the documented default", tokenEnv: "", want: "GOLEM_GITHUB_TOKEN"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h, mux := setupGitHubSyncTest(t)
			h.GitHubTokenEnv = tc.tokenEnv // h.Sync stays nil: sync is not running
			h.ManualSyncCooldown = time.Minute
			_, cookie := seedSessionUser(t, h.DB, "sync-admin", string(rbac.RoleAdmin))
			repo := seedSyncRepo(t, h, true, nil)

			req := httptest.NewRequest(http.MethodPost, syncURL(repo.ID), nil)
			withSession(req, cookie)
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)

			if rec.Code != http.StatusServiceUnavailable {
				t.Fatalf("status = %d, want 503: %s", rec.Code, rec.Body.String())
			}
			var body struct {
				Error string `json:"error"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("decode: %v", err)
			}
			if !strings.Contains(body.Error, tc.want) {
				t.Errorf("error = %q, want it to name %q", body.Error, tc.want)
			}
			if tc.tokenEnv != "" && strings.Contains(body.Error, "GOLEM_GITHUB_TOKEN") {
				t.Errorf("error names GOLEM_GITHUB_TOKEN on a deployment that renamed it: %q", body.Error)
			}
		})
	}
}

// TestManualSyncInvalidIDIsJSON covers the one refusal that never reaches a
// repo lookup.
func TestManualSyncInvalidIDIsJSON(t *testing.T) {
	h, mux := setupGitHubSyncTest(t)
	h.Sync = &fakeTrigger{ret: true}
	h.ManualSyncCooldown = time.Minute
	_, cookie := seedSessionUser(t, h.DB, "sync-admin", string(rbac.RoleAdmin))

	req := httptest.NewRequest(http.MethodPost, "/api/github/repos/not-a-number/sync", nil)
	withSession(req, cookie)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "application/json") {
		t.Fatalf("Content-Type = %q, want application/json", ct)
	}
}
