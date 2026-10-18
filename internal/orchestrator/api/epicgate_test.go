package api_test

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"gorm.io/gorm"

	"github.com/leonp92/golem/internal/epic"
	"github.com/leonp92/golem/internal/orchestrator/api"
	"github.com/leonp92/golem/internal/orchestrator/auth"
	"github.com/leonp92/golem/internal/orchestrator/db"
	"github.com/leonp92/golem/internal/orchestrator/epics"
	"github.com/leonp92/golem/internal/orchestrator/sse"
	ws "github.com/leonp92/golem/internal/orchestrator/ws"
)

// seedRunningEpic builds a two-node epic (A -> B), approves it and starts
// the run: child A is ready, child B is dependency-blocked.
func seedRunningEpic(t *testing.T, gdb *gorm.DB) *db.Epic {
	t.Helper()
	if err := gdb.Model(&db.Shem{}).Where("1 = 1").Update("supports_epics", true).Error; err != nil {
		t.Fatal(err)
	}
	e, err := epics.Create(gdb, epics.CreateParams{
		RepoRemote: "https://github.com/org/repo", Title: "Gate test",
		SpecMarkdown: "spec", BaseBranch: "main",
	})
	if err != nil {
		t.Fatalf("create epic: %v", err)
	}
	plan := epic.Plan{
		Requirements: []epic.Requirement{{ID: "R1", Text: "one"}, {ID: "R2", Text: "two"}},
		Nodes: []epic.Node{
			{Key: "A", Title: "first", AcceptanceCommands: []string{"go test ./..."},
				RequirementIDs: []string{"R1"}, BranchTarget: e.IntegrationRef},
			{Key: "B", Title: "second", AcceptanceCommands: []string{"go test ./..."},
				RequirementIDs: []string{"R2"}, BranchTarget: e.IntegrationRef},
		},
		Edges: []epic.Edge{{Predecessor: "A", Successor: "B", Reason: "B builds on A"}},
	}
	rev, err := epics.AddRevision(gdb, e.ID, plan, "")
	if err != nil {
		t.Fatalf("AddRevision: %v", err)
	}
	if err := epics.Approve(gdb, e.ID, rev.Revision, rev.PlanHash, "leon"); err != nil {
		t.Fatalf("Approve: %v", err)
	}
	if _, err := epics.Start(gdb, e.ID); err != nil {
		t.Fatalf("Start: %v", err)
	}
	return e
}

func epicChild(t *testing.T, gdb *gorm.DB, epicID, key string) db.Ticket {
	t.Helper()
	var c db.Ticket
	if err := gdb.First(&c, "epic_id = ? AND epic_node_key = ?", epicID, key).Error; err != nil {
		t.Fatalf("child %s: %v", key, err)
	}
	return c
}

// The pool paths: an epic child is listed and claimable only while its
// epic is running on the approved revision AND its predecessors are
// integrated. Ordinary tickets are untouched.
func TestEpicGate_AvailableAndClaim(t *testing.T) {
	h, mux := setupTicketTest(t)
	h.RegisterEpicRoutes(mux)
	seedShem(t, h, "shem-a", "testkey")
	e := seedRunningEpic(t, h.DB)
	ordinary := db.Ticket{ID: "plain-1", RepoRemote: "https://github.com/org/repo",
		BaseBranch: "main", Branch: "b", Title: "t", Description: "d", Phase: "unassigned"}
	h.DB.Create(&ordinary)

	available := func() []db.Ticket {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, shemRequest(http.MethodGet, "/api/tickets/available", "shem-a", "testkey"))
		if w.Code != http.StatusOK {
			t.Fatalf("available: %d", w.Code)
		}
		var got []db.Ticket
		if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
			t.Fatalf("decode: %v", err)
		}
		return got
	}
	ids := []string{}
	for _, tk := range available() {
		ids = append(ids, tk.ID)
	}
	// Ready child A and the ordinary ticket are listed; blocked B is not.
	want := map[string]bool{"plain-1": false, epicChild(t, h.DB, e.ID, "A").ID: false}
	for _, id := range ids {
		delete(want, id)
	}
	if len(want) != 0 {
		t.Fatalf("missing from available: %v (got %v)", want, ids)
	}
	if len(ids) != 2 {
		t.Fatalf("available = %v, want 2 tickets", ids)
	}

	// Claiming blocked B fails; claiming ready A succeeds.
	childB := epicChild(t, h.DB, e.ID, "B")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, shemRequest(http.MethodPost, "/api/tickets/"+childB.ID+"/claim", "shem-a", "testkey"))
	if w.Code != http.StatusConflict {
		t.Fatalf("claim blocked child = %d, want 409", w.Code)
	}
	childA := epicChild(t, h.DB, e.ID, "A")
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, shemRequest(http.MethodPost, "/api/tickets/"+childA.ID+"/claim", "shem-a", "testkey"))
	if w.Code != http.StatusOK {
		t.Fatalf("claim ready child = %d: %s", w.Code, w.Body.String())
	}

	// An epic that stops running releases nothing: flip the state and even
	// the ready-looking child vanishes from the pool.
	h.DB.Model(&db.Epic{}).Where("id = ?", e.ID).Update("state", "paused")
	if got := available(); len(got) != 1 || got[0].ID != "plain-1" {
		t.Fatalf("paused epic pool = %v", got)
	}
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, shemRequest(http.MethodPost, "/api/tickets/"+childB.ID+"/claim", "shem-a", "testkey"))
	if w.Code != http.StatusConflict {
		t.Fatalf("claim under paused epic = %d, want 409", w.Code)
	}
}

// The resume paths: an assigned child stays resumable while its epic runs
// on the approved revision, and drops out the moment that stops being
// true — a claim handed work to the agent, and the epic's approval is
// what keeps that hand-off valid.
func TestEpicGate_ResumeAndReclaim(t *testing.T) {
	h, mux := setupTicketTest(t)
	shem := seedShem(t, h, "shem-a", "testkey")
	e := seedRunningEpic(t, h.DB)
	childA := epicChild(t, h.DB, e.ID, "A")
	h.DB.Model(&db.Ticket{}).Where("id = ?", childA.ID).
		Updates(map[string]any{"phase": "implement", "assigned_shem": shem.ID})

	resumable := func() int {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, shemRequest(http.MethodGet, "/api/tickets/resumable", "shem-a", "testkey"))
		var got []api.ClaimResponse
		json.Unmarshal(w.Body.Bytes(), &got) //nolint:errcheck
		return len(got)
	}
	if n := resumable(); n != 1 {
		t.Fatalf("resumable while running = %d, want 1", n)
	}
	h.DB.Model(&db.Epic{}).Where("id = ?", e.ID).Update("state", "cancelled")
	if n := resumable(); n != 0 {
		t.Fatalf("resumable under cancelled epic = %d, want 0", n)
	}

	// Reclaim of a reaped child follows the same rule.
	h.DB.Model(&db.Epic{}).Where("id = ?", e.ID).Update("state", "running")
	h.DB.Model(&db.Ticket{}).Where("id = ?", childA.ID).Updates(map[string]any{
		"phase": "unassigned", "assigned_shem": nil,
		"reaped_from_shem": shem.ID, "reaped_from_phase": "implement",
	})
	reclaim := func() int {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, shemRequest(http.MethodPost, "/api/tickets/"+childA.ID+"/reclaim", "shem-a", "testkey"))
		return w.Code
	}
	if code := reclaim(); code != http.StatusNoContent {
		t.Fatalf("reclaim while running = %d", code)
	}
	h.DB.Model(&db.Ticket{}).Where("id = ?", childA.ID).Updates(map[string]any{
		"phase": "unassigned", "assigned_shem": nil,
		"reaped_from_shem": shem.ID, "reaped_from_phase": "implement",
	})
	h.DB.Model(&db.Epic{}).Where("id = ?", e.ID).Update("state", "cancelled")
	if code := reclaim(); code == http.StatusNoContent {
		t.Fatal("reclaim succeeded under cancelled epic")
	}
}

// The revision gate: a child materialized from revision 1 stops being
// claimable if the approved revision pointer moves (defense in depth —
// v1 blocks plan edits mid-run, and the predicate does not rely on that).
func TestEpicGate_RevisionMismatch(t *testing.T) {
	h, mux := setupTicketTest(t)
	seedShem(t, h, "shem-a", "testkey")
	e := seedRunningEpic(t, h.DB)
	childA := epicChild(t, h.DB, e.ID, "A")
	h.DB.Model(&db.Epic{}).Where("id = ?", e.ID).Update("approved_revision", 2)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, shemRequest(http.MethodPost, "/api/tickets/"+childA.ID+"/claim", "shem-a", "testkey"))
	if w.Code != http.StatusConflict {
		t.Fatalf("claim at stale revision = %d, want 409", w.Code)
	}
}

// The HTTP surface of run control: approve, start, integrate, watch the
// next child become claimable.
func TestEpicRunEndpoints(t *testing.T) {
	h, mux := setupTicketTest(t)
	h.RegisterEpicRoutes(mux)
	_, cookie := seedSessionUser(t, h.DB, "leon", "admin")
	e := seedRunningEpic(t, h.DB) // already started via the service

	// Start again over HTTP: idempotent, creates nothing.
	req := httptest.NewRequest(http.MethodPost, "/api/epics/"+e.ID+"/start", strings.NewReader("{}"))
	req.AddCookie(cookie)
	req.Header.Set("X-CSRF-Token", auth.CSRFTokenForSession(cookie.Value))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("start = %d: %s", w.Code, w.Body.String())
	}
	var out struct {
		Created int `json:"created"`
	}
	json.Unmarshal(w.Body.Bytes(), &out) //nolint:errcheck
	if out.Created != 0 {
		t.Fatalf("repeat start created %d", out.Created)
	}

	// Integration receipt over HTTP, then B is claimable.
	body := strings.NewReader(`{"sha":"abc123"}`)
	req = httptest.NewRequest(http.MethodPost, "/api/epics/"+e.ID+"/nodes/A/integrate", body)
	req.AddCookie(cookie)
	req.Header.Set("X-CSRF-Token", auth.CSRFTokenForSession(cookie.Value))
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusNoContent {
		t.Fatalf("integrate = %d: %s", w.Code, w.Body.String())
	}
	var b db.Ticket
	h.DB.First(&b, "epic_id = ? AND epic_node_key = 'B'", e.ID)
	if !b.EpicReady {
		t.Fatal("B not ready after A's receipt")
	}
}

// The claim payload hands the worker the run context it needs: which run
// and node the ticket is, and the run's base branch to resolve against
// while the integration ref does not exist yet.
func TestClaimResponseCarriesEpicContext(t *testing.T) {
	h, mux := setupTicketTest(t)
	seedShem(t, h, "shem-a", "testkey")
	e := seedRunningEpic(t, h.DB)
	childA := epicChild(t, h.DB, e.ID, "A")

	w := httptest.NewRecorder()
	mux.ServeHTTP(w, shemRequest(http.MethodPost, "/api/tickets/"+childA.ID+"/claim", "shem-a", "testkey"))
	if w.Code != http.StatusOK {
		t.Fatalf("claim = %d: %s", w.Code, w.Body.String())
	}
	var resp api.ClaimResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Epic == nil || resp.Epic.EpicID != e.ID || resp.Epic.NodeKey != "A" ||
		resp.Epic.BaseFallback != e.BaseBranch ||
		len(resp.Epic.AcceptanceCommands) != 1 || resp.Epic.AcceptanceCommands[0] != "go test ./..." {
		t.Fatalf("epic context = %+v", resp.Epic)
	}

	// An ordinary ticket's payload is unchanged: no epic key at all.
	plain := db.Ticket{ID: "plain-2", RepoRemote: "https://github.com/org/repo",
		BaseBranch: "main", Branch: "b", Title: "t", Description: "d", Phase: "unassigned"}
	h.DB.Create(&plain)
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, shemRequest(http.MethodPost, "/api/tickets/plain-2/claim", "shem-a", "testkey"))
	if w.Code != http.StatusOK {
		t.Fatalf("plain claim = %d", w.Code)
	}
	if strings.Contains(w.Body.String(), "epic") {
		t.Fatalf("ordinary ticket payload mentions epics: %s", w.Body.String())
	}
}

// The verdict endpoint is the tester runner's write path: API-key auth,
// validated shape, ownership of the child.
func TestPostEpicVerdict(t *testing.T) {
	h, mux := setupTicketTest(t)
	h.RegisterEpicRoutes(mux)
	seedShem(t, h, "shem-a", "testkey")
	e := seedRunningEpic(t, h.DB)
	seedShem(t, h, "shem-b", "otherkey")
	assignTicketToShem(t, h, epicChild(t, h.DB, e.ID, "A").ID, "shem-a")

	postAs := func(name, key, body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		req := shemRequest(http.MethodPost, "/api/epics/"+e.ID+"/verdicts", name, key)
		req.Body = io.NopCloser(strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		mux.ServeHTTP(w, req)
		return w
	}
	post := func(body string) *httptest.ResponseRecorder { return postAs("shem-a", "testkey", body) }
	// Only the shem holding the node's ticket may vouch for it, and the
	// final candidate (empty node key) is not a shem's to pass.
	if w := postAs("shem-b", "otherkey", `{"epic_node_key":"A","head_sha":"head1","verdict":"pass"}`); w.Code != http.StatusConflict {
		t.Fatalf("non-owner verdict: %d", w.Code)
	}
	if w := post(`{"epic_node_key":"","head_sha":"head1","verdict":"pass"}`); w.Code == http.StatusCreated {
		t.Fatal("final-candidate verdict accepted from a shem")
	}
	if w := post(`{"epic_node_key":"A","head_sha":"head1","verdict":"MAYBE"}`); w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("bad verdict: %d", w.Code)
	}
	w := post(`{"epic_node_key":"A","head_sha":"head1","base_sha":"base1","verdict":"pass","scenarios":"boot, login","backend":"local"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("post: %d %s", w.Code, w.Body.String())
	}
	var n int64
	h.DB.Model(&db.TesterVerdict{}).Where("epic_id = ? AND head_sha = 'head1' AND verdict = 'pass'", e.ID).Count(&n)
	if n != 1 {
		t.Fatalf("verdicts = %d", n)
	}
	// No API key, no write.
	w = httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/epics/"+e.ID+"/verdicts", strings.NewReader(`{}`))
	mux.ServeHTTP(w, req)
	if w.Code == http.StatusCreated {
		t.Fatal("unauthenticated verdict write")
	}
}

// The epic SQL (claim gates, idempotent start inside a transaction,
// receipts) on PostgreSQL, where a failed statement aborts the transaction.
func TestPostgresEpicRun(t *testing.T) {
	gdb := openPostgres(t)
	h := api.NewHandlers(gdb, ws.NewHub(), sse.NewBroker())
	mux := http.NewServeMux()
	h.RegisterTicketRoutes(mux)
	seedShem(t, h, "shem-a", "testkey")
	e := seedRunningEpic(t, gdb)
	if created, err := epics.Start(gdb, e.ID); err != nil || created != 0 {
		t.Fatalf("repeat start: created=%d err=%v", created, err)
	}
	b := epicChild(t, gdb, e.ID, "B")
	claim := func(id string) int {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, shemRequest(http.MethodPost, "/api/tickets/"+id+"/claim", "shem-a", "testkey"))
		return w.Code
	}
	if code := claim(b.ID); code != http.StatusConflict {
		t.Fatalf("blocked child claim = %d, want 409", code)
	}
	if err := epics.RecordIntegration(gdb, e.ID, "A", "sha-a"); err != nil {
		t.Fatalf("receipt: %v", err)
	}
	if code := claim(b.ID); code != http.StatusOK {
		t.Fatalf("unblocked child claim = %d, want 200", code)
	}
}

func TestLegacyShemCannotRunEpicChildren(t *testing.T) {
	h, mux := setupTicketTest(t)
	e := seedRunningEpic(t, h.DB)
	old := seedShem(t, h, "legacy", "key")
	a := epicChild(t, h.DB, e.ID, "A")
	plain := db.Ticket{ID: "legacy-ordinary", RepoRemote: e.RepoRemote, Branch: "plain", Phase: "unassigned"}
	if err := h.DB.Create(&plain).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := h.ClaimTicket(a.ID, old.ID); err == nil {
		t.Fatal("legacy worker claimed epic child")
	}
	if _, err := h.ClaimTicket(plain.ID, old.ID); err != nil {
		t.Fatalf("ordinary ticket rejected: %v", err)
	}
	call := func(path string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, shemRequest(http.MethodGet, path, "legacy", "key"))
		return w
	}
	if w := call("/api/tickets/available"); w.Code != 200 || strings.Contains(w.Body.String(), a.ID) {
		t.Fatalf("legacy available: %d %s", w.Code, w.Body.String())
	}
	h.DB.Model(&a).Updates(map[string]any{"assigned_shem": old.ID, "phase": "implement"})
	if w := call("/api/tickets/resumable"); w.Code != 200 || strings.Contains(w.Body.String(), a.ID) {
		t.Fatalf("legacy resumed epic: %s", w.Body.String())
	}
	h.DB.Model(&a).Update("phase", "revising")
	if _, err := h.ReviseClaim(a.ID, old.ID); err == nil {
		t.Fatal("legacy worker revised epic")
	}
	h.DB.Model(&a).Updates(map[string]any{"phase": "unassigned", "assigned_shem": nil, "reaped_from_shem": old.ID, "reaped_from_phase": "implement"})
	if w := call("/api/tickets/reaped"); w.Code != 200 || strings.Contains(w.Body.String(), a.ID) {
		t.Fatalf("legacy reaped: %d %s", w.Code, w.Body.String())
	}
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, shemRequest(http.MethodPost, "/api/tickets/"+a.ID+"/reclaim", "legacy", "key"))
	if w.Code != http.StatusConflict {
		t.Fatalf("legacy reclaim: %d %s", w.Code, w.Body.String())
	}
	// Reconnecting an old binary must clear a previously advertised capability.
	h.DB.Model(&old).Update("supports_epics", true)
	req := shemRequest(http.MethodPut, "/api/shems/me", "legacy", "key")
	req.Body = io.NopCloser(strings.NewReader(`{"name":"legacy","repos":[]}`))
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("legacy registration: %d %s", w.Code, w.Body.String())
	}
	h.DB.First(&old, old.ID)
	if old.SupportsEpics {
		t.Fatal("legacy registration retained epic capability")
	}
}

func TestPostgresSimultaneousIntegrationReceipts(t *testing.T) {
	gdb := openPostgres(t)
	e := seedRunningEpic(t, gdb)
	start := make(chan struct{})
	results := make(chan error, 2)
	for _, key := range []string{"A", "B"} {
		go func(key string) { <-start; results <- epics.RecordIntegration(gdb, e.ID, key, "merged-"+key) }(key)
	}
	close(start)
	for range 2 {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	var got db.Epic
	if err := gdb.First(&got, "id = ?", e.ID).Error; err != nil {
		t.Fatal(err)
	}
	if got.State != epics.StateAwaitingFinalReview {
		t.Fatalf("simultaneous receipts left epic %s", got.State)
	}
	for _, key := range []string{"A", "B"} {
		if child := epicChild(t, gdb, e.ID, key); child.EpicIntegratedSHA != "merged-"+key {
			t.Fatalf("lost receipt: %+v", child)
		}
	}
}

func TestPostgresIntegrationLeaseFencing(t *testing.T) {
	gdb := openPostgres(t)
	e := seedRunningEpic(t, gdb)
	sh := db.Shem{Name: "lease-holder", APIKeyHash: "x"}
	if err := gdb.Create(&sh).Error; err != nil {
		t.Fatal(err)
	}
	child := epicChild(t, gdb, e.ID, "A")
	gdb.Model(e).Updates(map[string]any{"auto_merge_children": true, "tester_required": true})
	gdb.Model(&child).Updates(map[string]any{"phase": "ready-for-review", "assigned_shem": sh.ID})
	if err := epics.RecordVerdict(gdb, db.TesterVerdict{EpicID: e.ID, EpicNodeKey: "A", HeadSHA: "head", Verdict: "pass"}); err != nil {
		t.Fatal(err)
	}
	first, err := epics.GateIntegration(gdb, e.ID, "A", "head", sh.ID, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	gdb.Model(&db.EpicIntegrationSlot{}).Where("epic_id = ?", e.ID).Update("expires_at", time.Now().Add(-time.Second))
	second, err := epics.GateIntegration(gdb, e.ID, "A", "head", sh.ID, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	pin := db.EpicIntegrationSlot{EpicID: e.ID, NodeKey: "A", HeadSHA: "head", ShemID: sh.ID, LeaseID: first.LeaseID}
	record := func(tx *gorm.DB, _ db.Ticket) error { return epics.RecordIntegration(tx, e.ID, "A", "merged") }
	if err := epics.ApplyIntegrationResult(gdb, pin, "merged", record); !errors.Is(err, epics.ErrIntegrationConflict) {
		t.Fatalf("stale lease=%v", err)
	}
	pin.LeaseID = second.LeaseID
	if err := epics.ApplyIntegrationResult(gdb, pin, "merged", record); err != nil {
		t.Fatal(err)
	}
	if err := epics.ApplyIntegrationResult(gdb, pin, "merged", record); err != nil {
		t.Fatalf("duplicate=%v", err)
	}
}
