package api_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"

	"github.com/leonp92/golem/internal/orchestrator/api"
	"github.com/leonp92/golem/internal/orchestrator/auth"
	"github.com/leonp92/golem/internal/orchestrator/db"
	"github.com/leonp92/golem/internal/orchestrator/ghsync"
	"github.com/leonp92/golem/internal/orchestrator/rbac"
	"github.com/leonp92/golem/internal/orchestrator/sse"
	ws "github.com/leonp92/golem/internal/orchestrator/ws"
)

func setupTicketTest(t *testing.T) (*api.Handlers, *http.ServeMux) {
	t.Helper()
	gdb, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	hub := ws.NewHub()
	h := api.NewHandlers(gdb, hub, sse.NewBroker())
	mux := http.NewServeMux()
	h.RegisterShemRoutes(mux)
	h.RegisterTicketRoutes(mux)
	h.RegisterLogRoutes(mux)
	h.RegisterHumanRoutes(mux)
	return h, mux
}

// seedSessionUser creates a db.User with the given role and an authenticated
// session cookie for it.
func seedSessionUser(t *testing.T, gdb *gorm.DB, username, role string) (db.User, *http.Cookie) {
	t.Helper()
	user := db.User{Username: username, PasswordHash: "x", Role: role}
	if err := gdb.Create(&user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	w := httptest.NewRecorder()
	if err := auth.CreateSession(gdb, w, user.ID, false); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	return user, w.Result().Cookies()[0]
}

// seedShem creates a Shem with a known API key and returns the key.
func seedShem(t *testing.T, h *api.Handlers, name, key string) db.Shem {
	t.Helper()
	hash, _ := bcrypt.GenerateFromPassword([]byte(key), bcrypt.MinCost)
	shem := db.Shem{Name: name, APIKeyHash: string(hash), Repos: "[]", Status: "offline"}
	h.DB.Create(&shem)
	return shem
}

func TestAvailableTickets(t *testing.T) {
	h, mux := setupTicketTest(t)

	// Seed a shem so RequireAPIKey works.
	seedShem(t, h, "shem-a", "testkey")

	// Seed an unassigned ticket.
	ticket := db.Ticket{
		RepoRemote:  "https://github.com/org/repo",
		Branch:      "ticket/available",
		Description: "available test",
		Phase:       "unassigned",
	}
	h.DB.Create(&ticket)

	req := httptest.NewRequest(http.MethodGet, "/api/tickets/available?repo=https://github.com/org/repo", nil)
	req.Header.Set("Authorization", "Bearer testkey")
	req.Header.Set("X-Shem-Name", "shem-a")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var tickets []db.Ticket
	if err := json.Unmarshal(w.Body.Bytes(), &tickets); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(tickets) != 1 {
		t.Errorf("expected 1 ticket, got %d", len(tickets))
	}
}

func TestClaimTicket_HTTPEndpoint(t *testing.T) {
	h, mux := setupTicketTest(t)
	shem := seedShem(t, h, "claimer", "claimkey")

	ticket := db.Ticket{
		RepoRemote:  "https://github.com/org/repo2",
		Branch:      "ticket/claim",
		Description: "claim test",
		Phase:       "unassigned",
	}
	h.DB.Create(&ticket)

	url := fmt.Sprintf("/api/tickets/%s/claim", ticket.ID)
	req := httptest.NewRequest(http.MethodPost, url, nil)
	req.Header.Set("Authorization", "Bearer claimkey")
	req.Header.Set("X-Shem-Name", "claimer")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	// Verify ticket is now claimed.
	var updated db.Ticket
	h.DB.First(&updated, "id = ?", ticket.ID)
	if updated.Phase != "claimed" {
		t.Errorf("expected phase=claimed, got %s", updated.Phase)
	}
	if updated.AssignedShem == nil || *updated.AssignedShem != shem.ID {
		t.Errorf("expected assigned_shem=%d, got %v", shem.ID, updated.AssignedShem)
	}
}

func TestUpdatePhase(t *testing.T) {
	h, mux := setupTicketTest(t)
	shem := seedShem(t, h, "phase-shem", "phasekey")

	ticket := db.Ticket{
		RepoRemote:   "https://github.com/org/repo3",
		Branch:       "ticket/phase",
		Description:  "phase test",
		Phase:        "claimed",
		AssignedShem: &shem.ID,
	}
	h.DB.Create(&ticket)

	body, _ := json.Marshal(map[string]string{"phase": "in-progress"})
	url := fmt.Sprintf("/api/tickets/%s/phase", ticket.ID)
	req := httptest.NewRequest(http.MethodPatch, url, bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer phasekey")
	req.Header.Set("X-Shem-Name", "phase-shem")
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d: %s", w.Code, w.Body.String())
	}
}

func TestReviseClaim_Success(t *testing.T) {
	h, _ := setupTicketTest(t)
	shem := seedShem(t, h, "revise-shem", "revisekey")

	sha := "abc123"
	ticket := db.Ticket{
		RepoRemote:    "https://github.com/org/repo-revise",
		Branch:        "ticket/revise",
		Description:   "revise test",
		Phase:         "revising",
		AssignedShem:  &shem.ID,
		CheckpointSHA: &sha,
	}
	h.DB.Create(&ticket)

	resp, err := h.ReviseClaim(ticket.ID, shem.ID)
	if err != nil {
		t.Fatalf("ReviseClaim: %v", err)
	}
	if resp.CheckpointPhase == nil || *resp.CheckpointPhase != "revising" {
		t.Errorf("expected checkpoint_phase=revising, got %v", resp.CheckpointPhase)
	}
	if resp.CheckpointSHA == nil || *resp.CheckpointSHA != sha {
		t.Errorf("expected checkpoint_sha=%s, got %v", sha, resp.CheckpointSHA)
	}
}

func TestReviseClaim_WrongShem_Conflict(t *testing.T) {
	h, _ := setupTicketTest(t)
	owner := seedShem(t, h, "owner-shem", "ownerkey")
	other := seedShem(t, h, "other-shem", "otherkey")

	ticket := db.Ticket{
		RepoRemote:   "https://github.com/org/repo-revise2",
		Branch:       "ticket/revise2",
		Description:  "revise test 2",
		Phase:        "revising",
		AssignedShem: &owner.ID,
	}
	h.DB.Create(&ticket)

	if _, err := h.ReviseClaim(ticket.ID, other.ID); err == nil {
		t.Fatal("expected error for wrong shem, got nil")
	}
}

func TestReviseClaim_WrongPhase_Conflict(t *testing.T) {
	h, _ := setupTicketTest(t)
	shem := seedShem(t, h, "revise-shem-3", "revisekey3")

	ticket := db.Ticket{
		RepoRemote:   "https://github.com/org/repo-revise3",
		Branch:       "ticket/revise3",
		Description:  "revise test 3",
		Phase:        "ready-for-review",
		AssignedShem: &shem.ID,
	}
	h.DB.Create(&ticket)

	if _, err := h.ReviseClaim(ticket.ID, shem.ID); err == nil {
		t.Fatal("expected error for wrong phase, got nil")
	}
}

func TestReviseClaim_HTTPEndpoint(t *testing.T) {
	h, mux := setupTicketTest(t)
	shem := seedShem(t, h, "revise-shem-4", "revisekey4")

	ticket := db.Ticket{
		RepoRemote:   "https://github.com/org/repo-revise4",
		Branch:       "ticket/revise4",
		Description:  "revise test 4",
		Phase:        "revising",
		AssignedShem: &shem.ID,
	}
	h.DB.Create(&ticket)

	url := fmt.Sprintf("/api/tickets/%s/revise-claim", ticket.ID)
	req := httptest.NewRequest(http.MethodPost, url, nil)
	req.Header.Set("Authorization", "Bearer revisekey4")
	req.Header.Set("X-Shem-Name", "revise-shem-4")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp api.ClaimResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.CheckpointPhase == nil || *resp.CheckpointPhase != "revising" {
		t.Errorf("expected checkpoint_phase=revising, got %v", resp.CheckpointPhase)
	}
}

func TestAppendLog(t *testing.T) {
	h, mux := setupTicketTest(t)
	seedShem(t, h, "log-shem", "logkey")

	ticket := db.Ticket{
		RepoRemote:  "https://github.com/org/repo4",
		Branch:      "ticket/log",
		Description: "log test",
		Phase:       "in-progress",
	}
	h.DB.Create(&ticket)
	assignTicketToShem(t, h, ticket.ID, "log-shem")

	body, _ := json.Marshal(map[string]string{
		"entry_type": "message",
		"message":    "hello from shem",
		"from_role":  "developer",
	})
	url := fmt.Sprintf("/api/tickets/%s/log", ticket.ID)
	req := httptest.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer logkey")
	req.Header.Set("X-Shem-Name", "log-shem")
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp map[string]uint
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp["sequence_num"] != 1 {
		t.Errorf("expected sequence_num=1, got %d", resp["sequence_num"])
	}
}

func TestCreateTicket_SetsCreatedByFromSession(t *testing.T) {
	h, mux := setupTicketTest(t)
	user, cookie := seedSessionUser(t, h.DB, "leon", string(rbac.RoleDeveloper))

	body, _ := json.Marshal(map[string]string{
		"repo_remote": "https://github.com/org/repo5",
		"branch":      "main",
		"title":       "Created By Test",
		"description": "created by test",
	})
	req := httptest.NewRequest(http.MethodPost, "/api/tickets", bytes.NewReader(body))
	withSession(req, cookie)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}
	var resp struct {
		CreatedByUserID *uint  `json:"created_by_user_id"`
		CreatedBy       string `json:"created_by"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.CreatedByUserID == nil || *resp.CreatedByUserID != user.ID {
		t.Errorf("expected created_by_user_id=%d, got %v", user.ID, resp.CreatedByUserID)
	}
	if resp.CreatedBy != "leon" {
		t.Errorf("expected created_by=leon, got %q", resp.CreatedBy)
	}
}

func TestCreateTicket_IgnoresClientSuppliedCreatedByUserID(t *testing.T) {
	h, mux := setupTicketTest(t)
	user, cookie := seedSessionUser(t, h.DB, "leon", string(rbac.RoleDeveloper))

	body, _ := json.Marshal(map[string]any{
		"repo_remote":        "https://github.com/org/repo6",
		"branch":             "main",
		"title":              "Spoof Test",
		"description":        "spoof test",
		"created_by_user_id": user.ID + 999,
	})
	req := httptest.NewRequest(http.MethodPost, "/api/tickets", bytes.NewReader(body))
	withSession(req, cookie)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}
	var resp struct {
		CreatedByUserID *uint `json:"created_by_user_id"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.CreatedByUserID == nil || *resp.CreatedByUserID != user.ID {
		t.Errorf("expected created_by_user_id to be the session user %d, got %v", user.ID, resp.CreatedByUserID)
	}
}

func TestListAndGetTicket_ReturnsCreatedBy(t *testing.T) {
	h, mux := setupTicketTest(t)
	user, cookie := seedSessionUser(t, h.DB, "leon", string(rbac.RoleDeveloper))

	owned := db.Ticket{
		RepoRemote:      "https://github.com/org/repo7",
		Branch:          "ticket/owned",
		Description:     "owned ticket",
		Phase:           "unassigned",
		CreatedByUserID: &user.ID,
	}
	h.DB.Create(&owned)
	legacy := db.Ticket{
		RepoRemote:  "https://github.com/org/repo8",
		Branch:      "ticket/legacy",
		Description: "legacy ticket",
		Phase:       "unassigned",
	}
	h.DB.Create(&legacy)

	req := httptest.NewRequest(http.MethodGet, "/api/tickets", nil)
	withSession(req, cookie)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var list []struct {
		ID        string `json:"id"`
		CreatedBy string `json:"created_by"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil {
		t.Fatalf("decode: %v", err)
	}
	byID := make(map[string]string, len(list))
	for _, t := range list {
		byID[t.ID] = t.CreatedBy
	}
	if byID[owned.ID] != "leon" {
		t.Errorf("expected owned ticket created_by=leon, got %q", byID[owned.ID])
	}
	if byID[legacy.ID] != "" {
		t.Errorf("expected legacy ticket created_by empty, got %q", byID[legacy.ID])
	}

	req = httptest.NewRequest(http.MethodGet, "/api/tickets/"+owned.ID, nil)
	withSession(req, cookie)
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var single struct {
		Ticket struct {
			CreatedBy string `json:"created_by"`
		} `json:"ticket"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &single); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if single.Ticket.CreatedBy != "leon" {
		t.Errorf("expected created_by=leon, got %q", single.Ticket.CreatedBy)
	}
}

func TestCreateTicket_RequiresTitle(t *testing.T) {
	h, mux := setupTicketTest(t)
	_, cookie := seedSessionUser(t, h.DB, "leon", string(rbac.RoleDeveloper))

	body, _ := json.Marshal(map[string]string{
		"repo_remote": "https://github.com/org/repo7",
		"branch":      "main",
		"description": "missing title",
	})
	req := httptest.NewRequest(http.MethodPost, "/api/tickets", bytes.NewReader(body))
	withSession(req, cookie)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
}

func TestCreateTicket_ComputesBranchFromTitle(t *testing.T) {
	h, mux := setupTicketTest(t)
	_, cookie := seedSessionUser(t, h.DB, "leon", string(rbac.RoleDeveloper))

	body, _ := json.Marshal(map[string]string{
		"repo_remote": "https://github.com/org/repo8",
		"branch":      "main",
		"title":       "Human Friendly Branch Names",
		"description": "branch computation test",
	})
	req := httptest.NewRequest(http.MethodPost, "/api/tickets", bytes.NewReader(body))
	withSession(req, cookie)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}
	var resp struct {
		ID     string `json:"id"`
		Branch string `json:"branch"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	want := "ticket/human-friendly-branch-names-" + resp.ID[:8]
	if resp.Branch != want {
		t.Errorf("expected branch=%q, got %q", want, resp.Branch)
	}
}

// TestSessionRoutes_UnknownRoleForbidden covers a row whose role is neither of
// the known ones (hand-edited, or written before the Role column existed and
// not yet bootstrapped): it must be refused, not admitted.
func TestSessionRoutes_UnknownRoleForbidden(t *testing.T) {
	h, mux := setupTicketTest(t)
	user, cookie := seedSessionUser(t, h.DB, "stranger", string(rbac.RoleDeveloper))
	if err := h.DB.Model(&user).Update("role", "").Error; err != nil {
		t.Fatalf("clear role: %v", err)
	}

	req := httptest.NewRequest("GET", "/api/tickets", nil)
	req.AddCookie(cookie)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Errorf("got %d, want 403", w.Code)
	}
}

// TestResumableTickets_ExcludesUnapprovedGitHubLinkedTicket pins the
// defence-in-depth clause fix round 4 added to resumableTickets. A non-nil
// assigned_shem is only ever set by ClaimTicket, which already requires
// (issue_number IS NULL OR intake_approved), so this state — claimed with a
// checkpoint, but not approved — should never arise through normal use; it
// is constructed directly here specifically to prove the added clause
// actually excludes it, rather than that invariant being an untested
// assumption ("nothing else writes assigned_shem").
func TestResumableTickets_ExcludesUnapprovedGitHubLinkedTicket(t *testing.T) {
	h, mux := setupTicketTest(t)
	shem := seedShem(t, h, "resumable-shem", "resumablekey")

	phase := "implement"
	nUnapproved, nApproved := 3, 4
	unapproved := db.Ticket{
		RepoRemote:      "https://github.com/org/repo",
		Branch:          "ticket/unapproved",
		Description:     "d",
		Phase:           phase,
		AssignedShem:    &shem.ID,
		CheckpointPhase: &phase,
		IssueNumber:     &nUnapproved,
		IntakeApproved:  false,
	}
	if err := h.DB.Create(&unapproved).Error; err != nil {
		t.Fatalf("seed unapproved ticket: %v", err)
	}
	// A REAL approval, i.e. what actionStart leaves behind: intake_approved
	// set AND approved_body_hash stamped from the description on the row.
	// This fixture predates body_hash and used to leave both hash columns
	// at '', which is the migrated shape re-review finding F1 is about —
	// the predicate's new approved_body_hash <> '' clause refuses it, quite
	// correctly. Stamping the hashes keeps the test asserting what it was
	// written to assert (an approved, checkpointed ticket resumes) instead
	// of accidentally asserting that empty hashes count as approval.
	approvedDesc := "d"
	approved := db.Ticket{
		RepoRemote:       "https://github.com/org/repo",
		Branch:           "ticket/approved",
		Description:      approvedDesc,
		Phase:            phase,
		AssignedShem:     &shem.ID,
		CheckpointPhase:  &phase,
		IssueNumber:      &nApproved,
		IntakeApproved:   true,
		BodyHash:         ghsync.HashDescription(approvedDesc),
		ApprovedBodyHash: ghsync.HashDescription(approvedDesc),
	}
	if err := h.DB.Create(&approved).Error; err != nil {
		t.Fatalf("seed approved ticket: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/tickets/resumable", nil)
	req.Header.Set("Authorization", "Bearer resumablekey")
	req.Header.Set("X-Shem-Name", "resumable-shem")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var claims []api.ClaimResponse
	if err := json.Unmarshal(w.Body.Bytes(), &claims); err != nil {
		t.Fatalf("decode: %v", err)
	}
	ids := map[string]bool{}
	for _, c := range claims {
		ids[c.TicketID] = true
	}
	if ids[unapproved.ID] {
		t.Error("unapproved GitHub-linked ticket appeared in /api/tickets/resumable")
	}
	if !ids[approved.ID] {
		t.Error("approved GitHub-linked ticket with a checkpoint did not appear in /api/tickets/resumable")
	}
}

// TestResumableTickets_IncludesAssignedTicketWithNoCheckpoint pins the fix
// for a ticket that nothing in the system could reach.
//
// resumableTickets used to require checkpoint_phase IS NOT NULL. A checkpoint
// is written when a phase COMPLETES, so a shem that restarted during the very
// first phase owned a ticket with none. That ticket was then in an active
// phase, so availableTickets would not offer it to anyone, and had no
// checkpoint, so its own shem would not resume it. Ticket 2de16a96 sat in
// brainstorm, assigned, for 1.8h on the first real deployment while the shem
// logged "waiting (no action needed)" — there was no operation, on any
// endpoint or in the UI, that would have moved it.
//
// Resuming without a checkpoint is well defined: RunTicket's
// CheckpointPhase == nil branch starts the phase over. The assertions below
// cover both halves — the ticket comes back, and it comes back with a nil
// checkpoint so the worker takes that branch rather than skipping phases.
func TestResumableTickets_IncludesAssignedTicketWithNoCheckpoint(t *testing.T) {
	h, mux := setupTicketTest(t)
	shem := seedShem(t, h, "nocheckpoint-shem", "nocheckpointkey")

	// The unreachable ticket: claimed, first phase never finished.
	fresh := db.Ticket{
		RepoRemote:   "https://github.com/org/repo",
		Branch:       "ticket/fresh",
		Description:  "d",
		Phase:        "brainstorm",
		AssignedShem: &shem.ID,
	}
	if err := h.DB.Create(&fresh).Error; err != nil {
		t.Fatalf("seed fresh ticket: %v", err)
	}
	// needs-attention must STAY excluded. Dropping the checkpoint requirement
	// widens this predicate, and needs-attention tickets are exactly the ones
	// that reach it without a checkpoint — a failed first phase. Without this
	// case the fix would resurrect the restart loop that ff2badb closed.
	attention := db.Ticket{
		RepoRemote:   "https://github.com/org/repo",
		Branch:       "ticket/attention",
		Description:  "d",
		Phase:        "needs-attention",
		AssignedShem: &shem.ID,
	}
	if err := h.DB.Create(&attention).Error; err != nil {
		t.Fatalf("seed needs-attention ticket: %v", err)
	}
	// The approval gate must still hold for a checkpointless ticket: dropping
	// one clause of a conjunction is the classic way to weaken another.
	n := 5
	unapproved := db.Ticket{
		RepoRemote:     "https://github.com/org/repo",
		Branch:         "ticket/unapproved-nocheckpoint",
		Description:    "d",
		Phase:          "brainstorm",
		AssignedShem:   &shem.ID,
		IssueNumber:    &n,
		IntakeApproved: false,
	}
	if err := h.DB.Create(&unapproved).Error; err != nil {
		t.Fatalf("seed unapproved ticket: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/tickets/resumable", nil)
	req.Header.Set("Authorization", "Bearer nocheckpointkey")
	req.Header.Set("X-Shem-Name", "nocheckpoint-shem")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var claims []api.ClaimResponse
	if err := json.Unmarshal(w.Body.Bytes(), &claims); err != nil {
		t.Fatalf("decode: %v", err)
	}
	byID := map[string]api.ClaimResponse{}
	for _, c := range claims {
		byID[c.TicketID] = c
	}

	got, ok := byID[fresh.ID]
	if !ok {
		t.Error("a ticket assigned to this shem in an active phase with no checkpoint " +
			"was not resumable; nothing else can reach it either, so it is stuck forever")
	} else if got.CheckpointPhase != nil {
		t.Errorf("resumed checkpointless ticket reported checkpoint_phase = %q; "+
			"the worker would skip phases that never ran", *got.CheckpointPhase)
	}
	if _, ok := byID[attention.ID]; ok {
		t.Error("needs-attention ticket became resumable: every shem restart would " +
			"re-run the work that already failed and needs a human")
	}
	if _, ok := byID[unapproved.ID]; ok {
		t.Error("unapproved GitHub-linked ticket with no checkpoint became resumable: " +
			"the intake approval gate is bypassed via resume")
	}
}
