package api_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"golang.org/x/crypto/bcrypt"

	"github.com/leonp92/golem/internal/orchestrator/api"
	"github.com/leonp92/golem/internal/orchestrator/db"
	"github.com/leonp92/golem/internal/orchestrator/epics"
	"github.com/leonp92/golem/internal/orchestrator/sse"
	ws "github.com/leonp92/golem/internal/orchestrator/ws"
)

func setupPlanJobTest(t *testing.T) (*api.Handlers, *http.ServeMux, *http.Cookie, string) {
	t.Helper()
	gdb, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	h := api.NewHandlers(gdb, ws.NewHub(), sse.NewBroker())
	mux := http.NewServeMux()
	h.RegisterEpicRoutes(mux)
	h.RegisterEpicPlanJobRoutes(mux)

	user := db.User{Username: "leon", Role: "admin"}
	gdb.Create(&user)

	hash, _ := bcrypt.GenerateFromPassword([]byte("shem-key"), bcrypt.MinCost)
	gdb.Create(&db.Shem{Name: "shem-1", APIKeyHash: string(hash),
		Repos: `["https://github.com/org/repo"]`, Status: "online", SupportsEpics: true})

	e, err := epics.Create(gdb, epics.CreateParams{
		RepoRemote: "https://github.com/org/repo", Title: "Workspaces", SpecMarkdown: "# spec",
	})
	if err != nil {
		t.Fatalf("create epic: %v", err)
	}
	return h, mux, makeSessionCookie(t, h, user.ID), e.ID
}

// shemRequest issues an API-key-authenticated request as the seeded shem.
func planJobShemRequest(t *testing.T, mux *http.ServeMux, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var reader *bytes.Reader
	if body != nil {
		data, _ := json.Marshal(body)
		reader = bytes.NewReader(data)
	} else {
		reader = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, path, reader)
	req.Header.Set("X-Shem-Name", "shem-1")
	req.Header.Set("Authorization", "Bearer shem-key")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func TestPlanJobRoundTripOverHTTP(t *testing.T) {
	_, mux, cookie, epicID := setupPlanJobTest(t)

	// Generate requires a session and CSRF; the shem key alone cannot.
	if rec := planJobShemRequest(t, mux, "POST", "/api/epics/"+epicID+"/plan/generate", nil); rec.Code == http.StatusAccepted {
		t.Fatalf("shem generate must be refused: code = %d", rec.Code)
	}
	rec := epicRequest(t, mux, cookie, "POST", "/api/epics/"+epicID+"/plan/generate", nil)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("generate: code = %d body = %s", rec.Code, rec.Body)
	}
	var job db.EpicPlanJob
	if err := json.NewDecoder(rec.Body).Decode(&job); err != nil {
		t.Fatalf("decode job: %v", err)
	}
	// A second request while active conflicts, not 500s.
	if rec := epicRequest(t, mux, cookie, "POST", "/api/epics/"+epicID+"/plan/generate", nil); rec.Code != http.StatusConflict {
		t.Fatalf("second generate: code = %d", rec.Code)
	}

	// The shem sees it available for its repo and claims it.
	if rec := planJobShemRequest(t, mux, "GET", "/api/epics/plan-jobs/available?repo=https://github.com/org/repo", nil); rec.Code != http.StatusOK {
		t.Fatalf("available: code = %d", rec.Code)
	}
	rec = planJobShemRequest(t, mux, "POST", "/api/epics/plan-jobs/"+job.ID+"/claim", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("claim: code = %d body = %s", rec.Code, rec.Body)
	}
	var payload epics.PlanJobPayload
	if err := json.NewDecoder(rec.Body).Decode(&payload); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if payload.BranchTarget != "epic/"+epicID || payload.SpecMarkdown != "# spec" {
		t.Errorf("payload wrong: %+v", payload)
	}

	// Progress, then a valid draft; the page's status endpoint shows it.
	if rec := planJobShemRequest(t, mux, "POST", "/api/epics/plan-jobs/"+job.ID+"/progress",
		map[string]any{"log_tail": "working", "new_attempt": true}); rec.Code != http.StatusNoContent {
		t.Fatalf("progress: code = %d body = %s", rec.Code, rec.Body)
	}
	plan := validPlanJSON()
	draft, _ := json.Marshal(plan)
	if rec := planJobShemRequest(t, mux, "POST", "/api/epics/plan-jobs/"+job.ID+"/result",
		map[string]any{"draft_json": string(draft)}); rec.Code != http.StatusNoContent {
		t.Fatalf("result: code = %d body = %s", rec.Code, rec.Body)
	}
	rec = epicRequest(t, mux, cookie, "GET", "/api/epics/"+epicID+"/plan/job", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("job status: code = %d", rec.Code)
	}
	if err := json.NewDecoder(rec.Body).Decode(&job); err != nil {
		t.Fatalf("decode status: %v", err)
	}
	if job.Status != epics.PlanJobDone || job.DraftJSON == "" {
		t.Fatalf("job = %+v", job)
	}
}

func TestPlanJobResultWithInvalidDraftFailsJobWithVisibleReason(t *testing.T) {
	_, mux, cookie, epicID := setupPlanJobTest(t)
	rec := epicRequest(t, mux, cookie, "POST", "/api/epics/"+epicID+"/plan/generate", nil)
	var job db.EpicPlanJob
	_ = json.NewDecoder(rec.Body).Decode(&job)
	planJobShemRequest(t, mux, "POST", "/api/epics/plan-jobs/"+job.ID+"/claim", nil)
	if rec := planJobShemRequest(t, mux, "POST", "/api/epics/plan-jobs/"+job.ID+"/result",
		map[string]any{"draft_json": `{"requirements":[],"nodes":[],"edges":[]}`}); rec.Code != http.StatusNoContent {
		t.Fatalf("result: code = %d", rec.Code)
	}
	rec = epicRequest(t, mux, cookie, "GET", "/api/epics/"+epicID+"/plan/job", nil)
	_ = json.NewDecoder(rec.Body).Decode(&job)
	if job.Status != epics.PlanJobFailed || job.Err == "" || job.DraftJSON != "" {
		t.Fatalf("job = %+v, want failed with a visible reason and no draft", job)
	}
}
