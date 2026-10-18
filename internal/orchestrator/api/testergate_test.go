package api_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/leonp92/golem/internal/orchestrator/api"
	"github.com/leonp92/golem/internal/orchestrator/db"
)

// The tester gate is a ticket-level flag: a standalone ticket that asks
// for it claims with tester_required set, and an ordinary ticket claims
// without it.
func TestClaimCarriesTicketTesterFlag(t *testing.T) {
	h, mux := setupTicketTest(t)
	seedShem(t, h, "shem-a", "testkey")
	withTester := db.Ticket{ID: "wants-tester", RepoRemote: "https://github.com/org/repo",
		BaseBranch: "main", Branch: "b1", Title: "t", Description: "d", Phase: "unassigned",
		TesterRequired: true}
	plain := db.Ticket{ID: "no-tester", RepoRemote: "https://github.com/org/repo",
		BaseBranch: "main", Branch: "b2", Title: "t", Description: "d", Phase: "unassigned"}
	h.DB.Create(&withTester)
	h.DB.Create(&plain)

	claim := func(id string) api.ClaimResponse {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, shemRequest(http.MethodPost, "/api/tickets/"+id+"/claim", "shem-a", "testkey"))
		if w.Code != http.StatusOK {
			t.Fatalf("claim %s = %d: %s", id, w.Code, w.Body.String())
		}
		var resp api.ClaimResponse
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatalf("decode: %v", err)
		}
		return resp
	}
	if resp := claim("wants-tester"); !resp.TesterRequired {
		t.Fatal("ticket-level tester flag lost at claim")
	}
	if resp := claim("no-tester"); resp.TesterRequired {
		t.Fatal("ordinary ticket gained the tester gate")
	}
}

// Children materialized before the ticket-level flag existed keep their
// gate through the run's policy, OR-ed into the claim.
func TestClaimKeepsRunPolicyForLegacyChildren(t *testing.T) {
	h, mux := setupTicketTest(t)
	seedShem(t, h, "shem-a", "testkey")
	e := seedRunningEpic(t, h.DB)
	if err := h.DB.Model(&db.Epic{}).Where("id = ?", e.ID).Update("tester_required", true).Error; err != nil {
		t.Fatal(err)
	}
	child := epicChild(t, h.DB, e.ID, "A")
	if child.TesterRequired {
		t.Fatal("precondition: child without the ticket flag")
	}
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, shemRequest(http.MethodPost, "/api/tickets/"+child.ID+"/claim", "shem-a", "testkey"))
	if w.Code != http.StatusOK {
		t.Fatalf("claim = %d: %s", w.Code, w.Body.String())
	}
	var resp api.ClaimResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !resp.TesterRequired || resp.Epic == nil || !resp.Epic.TesterRequired {
		t.Fatalf("run policy did not back up the legacy child: %+v", resp)
	}
}

// The standalone-ticket verdict endpoint is the tester runner's write path
// for non-epic tickets: API-key auth, ownership of the ticket, validated
// shape, and no verdicts for epic children (they report to their run).
func TestPostTicketVerdict(t *testing.T) {
	h, mux := setupTicketTest(t)
	seedShem(t, h, "shem-a", "testkey")
	seedShem(t, h, "shem-b", "otherkey")
	ticket := db.Ticket{ID: "solo-1", RepoRemote: "https://github.com/org/repo",
		BaseBranch: "main", Branch: "b", Title: "t", Description: "d", Phase: "implement",
		TesterRequired: true}
	h.DB.Create(&ticket)
	assignTicketToShem(t, h, "solo-1", "shem-a")
	e := seedRunningEpic(t, h.DB)
	child := epicChild(t, h.DB, e.ID, "A")
	assignTicketToShem(t, h, child.ID, "shem-a")

	postAs := func(name, key, id, body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		req := shemRequest(http.MethodPost, "/api/tickets/"+id+"/tester-verdict", name, key)
		req.Body = io.NopCloser(strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		mux.ServeHTTP(w, req)
		return w
	}
	post := func(body string) *httptest.ResponseRecorder { return postAs("shem-a", "testkey", "solo-1", body) }

	if w := postAs("shem-b", "otherkey", "solo-1", `{"head_sha":"head1","verdict":"pass"}`); w.Code != http.StatusConflict {
		t.Fatalf("non-owner verdict: %d", w.Code)
	}
	if w := post(`{"head_sha":"head1","verdict":"MAYBE"}`); w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("bad verdict: %d", w.Code)
	}
	if w := post(`{"verdict":"pass"}`); w.Code != http.StatusBadRequest {
		t.Fatalf("missing head: %d", w.Code)
	}
	if w := postAs("shem-a", "testkey", child.ID, `{"head_sha":"head1","verdict":"pass"}`); w.Code != http.StatusConflict {
		t.Fatalf("epic child on the ticket endpoint: %d", w.Code)
	}
	w := post(`{"head_sha":"head1","base_sha":"base1","verdict":"pass","scenarios":"boot, login","backend":"local"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("post: %d %s", w.Code, w.Body.String())
	}
	var n int64
	h.DB.Model(&db.TesterVerdict{}).Where("ticket_id = 'solo-1' AND epic_id = '' AND head_sha = 'head1' AND verdict = 'pass'").Count(&n)
	if n != 1 {
		t.Fatalf("verdicts = %d", n)
	}
	// No API key, no write.
	w = httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/tickets/solo-1/tester-verdict", strings.NewReader(`{}`))
	mux.ServeHTTP(w, req)
	if w.Code == http.StatusCreated {
		t.Fatal("unauthenticated verdict write")
	}
}

func TestCreateTicketPreservesVerificationOptions(t *testing.T) {
	h, mux := setupTicketTest(t)
	_, cookie := seedSessionUser(t, h.DB, "creator", "developer")
	req := httptest.NewRequest(http.MethodPost, "/api/tickets", strings.NewReader(`{"repo_remote":"https://github.com/org/repo","branch":"main","title":"options","description":"d","tester_required":true,"verify_disabled":true}`))
	withSession(req, cookie)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}
	var got db.Ticket
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if !got.TesterRequired || !got.VerifyDisabled {
		t.Fatalf("creation discarded options: %+v", got)
	}
	var stored db.Ticket
	if err := h.DB.First(&stored, "id = ?", got.ID).Error; err != nil {
		t.Fatal(err)
	}
	if !stored.TesterRequired || !stored.VerifyDisabled {
		t.Fatal("options were not persisted")
	}
}
