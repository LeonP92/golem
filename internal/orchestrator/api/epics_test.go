package api_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/leonp92/golem/internal/epic"
	"github.com/leonp92/golem/internal/orchestrator/api"
	"github.com/leonp92/golem/internal/orchestrator/auth"
	"github.com/leonp92/golem/internal/orchestrator/db"
	"github.com/leonp92/golem/internal/orchestrator/sse"
	ws "github.com/leonp92/golem/internal/orchestrator/ws"
)

func setupEpicTest(t *testing.T) (*api.Handlers, *http.ServeMux, *http.Cookie) {
	t.Helper()
	gdb, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	h := api.NewHandlers(gdb, ws.NewHub(), sse.NewBroker())
	mux := http.NewServeMux()
	h.RegisterEpicRoutes(mux)

	user := db.User{Username: "leon", Role: "admin"}
	gdb.Create(&user)
	return h, mux, makeSessionCookie(t, h, user.ID)
}

func epicRequest(t *testing.T, mux *http.ServeMux, cookie *http.Cookie, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var reader *bytes.Reader
	if body != nil {
		data, _ := json.Marshal(body)
		reader = bytes.NewReader(data)
	} else {
		reader = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, path, reader)
	req.AddCookie(cookie)
	req.Header.Set("X-CSRF-Token", auth.CSRFTokenForSession(cookie.Value))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func validPlanJSON() epic.Plan {
	return epic.Plan{
		Requirements: []epic.Requirement{{ID: "R1", Text: "model"}, {ID: "R2", Text: "api"}},
		Nodes: []epic.Node{
			{Key: "T1", Title: "schema", AcceptanceCommands: []string{"go test ./..."},
				RequirementIDs: []string{"R1"}},
			{Key: "T2", Title: "api", AcceptanceCommands: []string{"go test ./api/..."},
				RequirementIDs: []string{"R2"}},
		},
		Edges: []epic.Edge{{Predecessor: "T1", Successor: "T2", Reason: "api uses model"}},
	}
}

func createTestEpic(t *testing.T, mux *http.ServeMux, cookie *http.Cookie) db.Epic {
	t.Helper()
	rec := epicRequest(t, mux, cookie, "POST", "/api/epics", map[string]string{
		"repo_remote": "https://github.com/org/repo", "title": "Workspaces", "spec_markdown": "# spec",
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create epic: %d %s", rec.Code, rec.Body.String())
	}
	var e db.Epic
	if err := json.Unmarshal(rec.Body.Bytes(), &e); err != nil {
		t.Fatalf("decode epic: %v", err)
	}
	return e
}

// End to end over HTTP: create, add a valid revision, approve
// the exact hash, and see the approval on the epic.
func TestEpicLifecycleOverHTTP(t *testing.T) {
	h, mux, cookie := setupEpicTest(t)
	e := createTestEpic(t, mux, cookie)

	rec := epicRequest(t, mux, cookie, "POST", "/api/epics/"+e.ID+"/revisions", validPlanJSON())
	if rec.Code != http.StatusCreated {
		t.Fatalf("add revision: %d %s", rec.Code, rec.Body.String())
	}
	var rev db.EpicPlanRevision
	json.Unmarshal(rec.Body.Bytes(), &rev)

	rec = epicRequest(t, mux, cookie, "POST", "/api/epics/"+e.ID+"/approve",
		map[string]any{"revision": rev.Revision, "plan_hash": "bogus"})
	if rec.Code != http.StatusConflict {
		t.Errorf("approve with wrong hash: %d, want 409", rec.Code)
	}
	rec = epicRequest(t, mux, cookie, "POST", "/api/epics/"+e.ID+"/approve",
		map[string]any{"revision": rev.Revision, "plan_hash": rev.PlanHash})
	if rec.Code != http.StatusNoContent {
		t.Fatalf("approve: %d %s", rec.Code, rec.Body.String())
	}

	rec = epicRequest(t, mux, cookie, "GET", "/api/epics/"+e.ID, nil)
	var detail struct {
		Epic db.Epic   `json:"epic"`
		Plan epic.Plan `json:"plan"`
	}
	json.Unmarshal(rec.Body.Bytes(), &detail)
	if detail.Epic.ApprovedPlanHash != rev.PlanHash {
		t.Errorf("approved hash = %q, want %q", detail.Epic.ApprovedPlanHash, rev.PlanHash)
	}
	var stored db.EpicPlanRevision
	h.DB.First(&stored, "epic_id = ?", e.ID)
	if stored.ApprovedBy != "leon" {
		t.Errorf("approved_by = %q, want the username", stored.ApprovedBy)
	}
	if len(detail.Plan.Nodes) != 2 {
		t.Errorf("detail plan has %d nodes, want 2", len(detail.Plan.Nodes))
	}
}

// Invalid plans are 422 with the problems listed; nothing is stored.
func TestAddRevisionRejectsInvalidPlanOverHTTP(t *testing.T) {
	h, mux, cookie := setupEpicTest(t)
	e := createTestEpic(t, mux, cookie)
	bad := validPlanJSON()
	bad.Edges = append(bad.Edges, epic.Edge{Predecessor: "T2", Successor: "T1", Reason: "loop"})
	rec := epicRequest(t, mux, cookie, "POST", "/api/epics/"+e.ID+"/revisions", bad)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("cyclic plan: %d, want 422", rec.Code)
	}
	var count int64
	h.DB.Model(&db.EpicPlanRevision{}).Count(&count)
	if count != 0 {
		t.Errorf("invalid plan stored %d revisions", count)
	}
}

// Writes require a session: no anonymous epic creation.
func TestEpicRoutesRequireSession(t *testing.T) {
	_, mux, _ := setupEpicTest(t)
	req := httptest.NewRequest("POST", "/api/epics", bytes.NewReader([]byte(`{}`)))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code == http.StatusCreated {
		t.Error("anonymous request created an epic")
	}
}
