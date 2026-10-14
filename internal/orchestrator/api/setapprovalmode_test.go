package api_test

import (
	"net/http"
	"net/url"
	"testing"

	"github.com/leonp92/golem/internal/orchestrator/db"
)

func TestSetApprovalModeOnAnUnclaimedTicket(t *testing.T) {
	h, mux, cookie, ticket := setModelsFixture(t)
	if reload(t, h, ticket.ID).ManualApproval {
		t.Fatal("a new ticket must default to automatic approval")
	}
	w := postModelAction(t, mux, cookie, ticket.ID, url.Values{
		"action": {"set-approval-mode"}, "manual_approval": {"true"}})
	if w.Code != http.StatusNoContent {
		t.Fatalf("status = %d: %s", w.Code, w.Body.String())
	}
	if !reload(t, h, ticket.ID).ManualApproval {
		t.Fatal("manual_approval not stored")
	}
	w = postModelAction(t, mux, cookie, ticket.ID, url.Values{
		"action": {"set-approval-mode"}, "manual_approval": {"false"}})
	if w.Code != http.StatusNoContent || reload(t, h, ticket.ID).ManualApproval {
		t.Fatalf("switch back to automatic failed: %d", w.Code)
	}
}

func TestSetApprovalModeRequiresAnExplicitValue(t *testing.T) {
	h, mux, cookie, ticket := setModelsFixture(t)
	h.DB.Model(&db.Ticket{}).Where("id = ?", ticket.ID).Update("manual_approval", true)
	// An absent value must not be read as "turn approvals off".
	w := postModelAction(t, mux, cookie, ticket.ID, url.Values{"action": {"set-approval-mode"}})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
	if !reload(t, h, ticket.ID).ManualApproval {
		t.Fatal("an invalid request changed the mode")
	}
}

func TestSetApprovalModeIsRefusedOnAClaimedOrClosedTicket(t *testing.T) {
	h, mux, cookie, ticket := setModelsFixture(t)
	var shem db.Shem
	if err := h.DB.First(&shem).Error; err != nil {
		t.Fatal(err)
	}
	h.DB.Model(&db.Ticket{}).Where("id = ?", ticket.ID).Update("assigned_shem", shem.ID)
	w := postModelAction(t, mux, cookie, ticket.ID, url.Values{
		"action": {"set-approval-mode"}, "manual_approval": {"true"}})
	if w.Code != http.StatusConflict {
		t.Fatalf("claimed: status = %d, want 409", w.Code)
	}
	h.DB.Model(&db.Ticket{}).Where("id = ?", ticket.ID).Updates(map[string]any{"assigned_shem": nil, "phase": "closed"})
	w = postModelAction(t, mux, cookie, ticket.ID, url.Values{
		"action": {"set-approval-mode"}, "manual_approval": {"true"}})
	if w.Code != http.StatusConflict {
		t.Fatalf("closed: status = %d, want 409", w.Code)
	}
	if reload(t, h, ticket.ID).ManualApproval {
		t.Fatal("mode changed on a claimed or closed ticket")
	}
}
