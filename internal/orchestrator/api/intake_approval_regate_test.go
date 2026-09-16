package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"gorm.io/gorm"

	"github.com/leonp92/golem/internal/github"
	"github.com/leonp92/golem/internal/orchestrator/db"
	"github.com/leonp92/golem/internal/orchestrator/ghsync"
)

// seedGitHubRepo creates an enabled db.GitHubRepo row for ghsync.IngestRepo
// to sync against.
func seedGitHubRepo(t *testing.T, gdb *gorm.DB, repoRemote string) *db.GitHubRepo {
	t.Helper()
	repo := db.GitHubRepo{RepoRemote: repoRemote, Owner: "org", Name: "repo", Enabled: true, Label: "golem"}
	if err := gdb.Create(&repo).Error; err != nil {
		t.Fatalf("seed GitHubRepo: %v", err)
	}
	return &repo
}

// doStart posts action=start to ticketID via the real HTTP action endpoint.
func doStart(t *testing.T, mux *http.ServeMux, cookie *http.Cookie, ticketID string) *httptest.ResponseRecorder {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"action": "start"})
	req := httptest.NewRequest(http.MethodPost, "/api/tickets/"+ticketID+"/actions", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(cookie)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	return w
}

// TestPostApprovalIssueEditRegatesUnclaimedTicket is the fix-round-4 test for
// binding an intake approval to the exact text a human reviewed (spec
// Amendment 1). Without this, intake_approved meant only "a human pressed
// Approve on this ticket at some point", not "on this text": ghsync
// overwrites Description from the live issue on every poll with no re-gate,
// so approve -> attacker edits the issue -> next poll would otherwise hand
// the agent prompt-builder text nobody ever reviewed.
//
// This drives the real HTTP action endpoint for the approval (not a direct
// DB write) and the real ghsync.IngestRepo for both polls, then checks
// claimability through the real GET /api/tickets/available endpoint — not
// merely that a phase string or a boolean flipped — so it is evidence the
// re-gate actually fires end to end, not just that the code compiles.
func TestPostApprovalIssueEditRegatesUnclaimedTicket(t *testing.T) {
	h, mux := setupTicketTest(t)
	seedShem(t, h, "regate-shem", "regatekey")
	_, cookie := seedSessionUser(t, h.DB, "regate-admin")

	const repoRemote = "https://github.com/org/repo"
	repo := seedGitHubRepo(t, h.DB, repoRemote)

	fake := github.NewFake()
	const originalBody = "please add rate limiting to the login endpoint"
	fake.AddIssue(github.Issue{Number: 55, Title: "Add rate limiting", Body: originalBody,
		State: "open", UpdatedAt: time.Now(), Labels: []string{"golem"}})

	syncer := ghsync.NewSyncer(h.DB, fake)
	if err := syncer.IngestRepo(context.Background(), repo); err != nil {
		t.Fatalf("initial IngestRepo: %v", err)
	}

	var ticket db.Ticket
	if err := h.DB.Where("repo_remote = ? AND issue_number = ?", repoRemote, 55).First(&ticket).Error; err != nil {
		t.Fatalf("ticket not created: %v", err)
	}
	if ticket.Phase != "pending-approval" {
		t.Fatalf("phase = %q, want pending-approval before approval", ticket.Phase)
	}

	if w := doStart(t, mux, cookie, ticket.ID); w.Code != http.StatusNoContent {
		t.Fatalf("start: expected 204, got %d: %s", w.Code, w.Body.String())
	}

	var approved db.Ticket
	if err := h.DB.First(&approved, "id = ?", ticket.ID).Error; err != nil {
		t.Fatalf("reload approved ticket: %v", err)
	}
	if !approved.IntakeApproved || approved.ApprovedBodyHash == "" {
		t.Fatalf("ticket not recorded as approved: intake_approved=%v approved_body_hash=%q",
			approved.IntakeApproved, approved.ApprovedBodyHash)
	}

	// Attacker (or anyone who can edit the issue) mutates the body after
	// approval. A later UpdatedAt is required for ListIssuesSince to surface
	// it past the cursor the first poll advanced.
	const maliciousBody = "MALICIOUS BODY NOBODY REVIEWED"
	fake.AddIssue(github.Issue{Number: 55, Title: "Add rate limiting", Body: maliciousBody,
		State: "open", UpdatedAt: time.Now().Add(time.Hour), Labels: []string{"golem"}})

	if err := syncer.IngestRepo(context.Background(), repo); err != nil {
		t.Fatalf("second IngestRepo: %v", err)
	}

	var got db.Ticket
	if err := h.DB.First(&got, "id = ?", ticket.ID).Error; err != nil {
		t.Fatalf("reload ticket after edit: %v", err)
	}
	if got.Phase != "pending-approval" {
		t.Errorf("phase = %q, want pending-approval after a post-approval edit", got.Phase)
	}
	if got.IntakeApproved {
		t.Error("intake_approved = true, want false after a post-approval edit")
	}
	if got.ApprovedBodyHash != "" {
		t.Errorf("approved_body_hash = %q, want cleared", got.ApprovedBodyHash)
	}
	if got.Description != maliciousBody {
		t.Errorf("description = %q, want the new (source-of-truth) body", got.Description)
	}

	// The property that actually matters: absent from the real available
	// endpoint, exactly as an ingested-but-never-approved ticket would be.
	availReq := httptest.NewRequest(http.MethodGet, "/api/tickets/available?repo="+repoRemote, nil)
	availReq.Header.Set("Authorization", "Bearer regatekey")
	availReq.Header.Set("X-Shem-Name", "regate-shem")
	availW := httptest.NewRecorder()
	mux.ServeHTTP(availW, availReq)
	var available []db.Ticket
	if err := json.Unmarshal(availW.Body.Bytes(), &available); err != nil {
		t.Fatalf("decode available: %v", err)
	}
	for _, at := range available {
		if at.ID == ticket.ID {
			t.Fatal("re-gated ticket appeared in /api/tickets/available — the edit was not re-gated")
		}
	}

	// Claim must also be rejected directly (belt and braces beyond available).
	claimReq := httptest.NewRequest(http.MethodPost, "/api/tickets/"+ticket.ID+"/claim", nil)
	claimReq.Header.Set("Authorization", "Bearer regatekey")
	claimReq.Header.Set("X-Shem-Name", "regate-shem")
	claimW := httptest.NewRecorder()
	mux.ServeHTTP(claimW, claimReq)
	if claimW.Code != http.StatusConflict {
		t.Errorf("claim: expected 409, got %d: %s", claimW.Code, claimW.Body.String())
	}

	var logs []db.LogEntry
	h.DB.Where("ticket_id = ? AND entry_type = 'STATUS'", ticket.ID).Find(&logs)
	found := false
	for _, l := range logs {
		if l.Message == "Issue body changed after approval; a human must re-approve before this ticket can be claimed." {
			found = true
		}
	}
	if !found {
		t.Errorf("expected a STATUS log entry announcing the re-gate, got %+v", logs)
	}
}

// TestPostApprovalIssueEditDoesNotYankClaimedTicket is the converse of the
// test above: once a ticket has been claimed, the assigned shem already has
// the previously-approved body and may be mid-run. A post-approval edit must
// not yank the ticket back to pending-approval out from under it — only be
// logged loudly.
func TestPostApprovalIssueEditDoesNotYankClaimedTicket(t *testing.T) {
	h, mux := setupTicketTest(t)
	shem := seedShem(t, h, "claim-regate-shem", "claimregatekey")
	_, cookie := seedSessionUser(t, h.DB, "claim-regate-admin")

	const repoRemote = "https://github.com/org/repo"
	repo := seedGitHubRepo(t, h.DB, repoRemote)

	fake := github.NewFake()
	const originalBody = "please add rate limiting to the login endpoint"
	fake.AddIssue(github.Issue{Number: 56, Title: "Add rate limiting", Body: originalBody,
		State: "open", UpdatedAt: time.Now(), Labels: []string{"golem"}})

	syncer := ghsync.NewSyncer(h.DB, fake)
	if err := syncer.IngestRepo(context.Background(), repo); err != nil {
		t.Fatalf("initial IngestRepo: %v", err)
	}

	var ticket db.Ticket
	if err := h.DB.Where("repo_remote = ? AND issue_number = ?", repoRemote, 56).First(&ticket).Error; err != nil {
		t.Fatalf("ticket not created: %v", err)
	}

	if w := doStart(t, mux, cookie, ticket.ID); w.Code != http.StatusNoContent {
		t.Fatalf("start: expected 204, got %d: %s", w.Code, w.Body.String())
	}

	var approved db.Ticket
	if err := h.DB.First(&approved, "id = ?", ticket.ID).Error; err != nil {
		t.Fatalf("reload approved ticket: %v", err)
	}
	approvedHash := approved.ApprovedBodyHash

	claimReq := httptest.NewRequest(http.MethodPost, "/api/tickets/"+ticket.ID+"/claim", nil)
	claimReq.Header.Set("Authorization", "Bearer claimregatekey")
	claimReq.Header.Set("X-Shem-Name", "claim-regate-shem")
	claimW := httptest.NewRecorder()
	mux.ServeHTTP(claimW, claimReq)
	if claimW.Code != http.StatusOK {
		t.Fatalf("claim: expected 200, got %d: %s", claimW.Code, claimW.Body.String())
	}

	var claimed db.Ticket
	if err := h.DB.First(&claimed, "id = ?", ticket.ID).Error; err != nil {
		t.Fatalf("reload claimed ticket: %v", err)
	}
	if claimed.Phase != "claimed" || claimed.AssignedShem == nil || *claimed.AssignedShem != shem.ID {
		t.Fatalf("ticket not claimed as expected: phase=%q assigned_shem=%v", claimed.Phase, claimed.AssignedShem)
	}

	const editedBody = "an edit made after this ticket was already claimed"
	fake.AddIssue(github.Issue{Number: 56, Title: "Add rate limiting", Body: editedBody,
		State: "open", UpdatedAt: time.Now().Add(time.Hour), Labels: []string{"golem"}})

	if err := syncer.IngestRepo(context.Background(), repo); err != nil {
		t.Fatalf("second IngestRepo: %v", err)
	}

	var got db.Ticket
	if err := h.DB.First(&got, "id = ?", ticket.ID).Error; err != nil {
		t.Fatalf("reload ticket after edit: %v", err)
	}
	if got.Phase != "claimed" {
		t.Errorf("phase = %q, want unchanged claimed — a claimed ticket must not be yanked", got.Phase)
	}
	if !got.IntakeApproved {
		t.Error("intake_approved = false, want unchanged true — a claimed ticket must not be yanked")
	}
	if got.ApprovedBodyHash != approvedHash {
		t.Errorf("approved_body_hash changed from %q to %q, want unchanged — a claimed ticket's "+
			"approval record must not be touched", approvedHash, got.ApprovedBodyHash)
	}
	if got.AssignedShem == nil || *got.AssignedShem != shem.ID {
		t.Error("assigned_shem cleared or changed, want unchanged")
	}
	if got.Description != editedBody {
		t.Errorf("description = %q, want the new body — GitHub is still the source of truth for it", got.Description)
	}

	var logs []db.LogEntry
	h.DB.Where("ticket_id = ? AND entry_type = 'STATUS'", ticket.ID).Find(&logs)
	found := false
	for _, l := range logs {
		if l.Message == "Issue body changed after approval; this ticket is already claimed, so the running agent is still working from the previously approved text." {
			found = true
		}
	}
	if !found {
		t.Errorf("expected a STATUS log entry noting the post-approval edit on a claimed ticket, got %+v", logs)
	}
}
