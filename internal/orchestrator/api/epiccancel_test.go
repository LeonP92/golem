package api_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/leonp92/golem/internal/orchestrator/auth"
	"github.com/leonp92/golem/internal/orchestrator/db"
	"github.com/leonp92/golem/internal/orchestrator/epics"
	"github.com/leonp92/golem/internal/orchestrator/ghsync"
	ws "github.com/leonp92/golem/internal/orchestrator/ws"
)

// Cancelling a running run over HTTP closes every open child the way the
// ticket close action does: the shem holding one is told to stop, a linked
// issue's close is queued, open questions go away, and each child's log says
// why. A repeat is a quiet success.
func TestCancelEpicOverHTTP(t *testing.T) {
	h, mux, cookie := setupEpicTest(t)
	e := seedRunningEpic(t, h.DB)
	shem := db.Shem{Name: "worker", APIKeyHash: "x", Repos: "[]", Status: "online"}
	h.DB.Create(&shem)
	a, b := epicChild(t, h.DB, e.ID, "A"), epicChild(t, h.DB, e.ID, "B")
	issue := 12
	h.DB.Model(&db.Ticket{}).Where("id = ?", a.ID).Updates(map[string]any{"phase": "implement", "assigned_shem": shem.ID})
	h.DB.Model(&db.Ticket{}).Where("id = ?", b.ID).Update("issue_number", issue)
	h.DB.Create(&db.HumanInput{TicketID: b.ID, Kind: "question", Prompt: "which?", CreatedAt: time.Now()})
	client := connectFakeShem(t, h.Hub, shem.ID)

	rec := epicRequest(t, mux, cookie, "POST", "/api/epics/"+e.ID+"/cancel", map[string]string{"reason": "scrapped"})
	if rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != `{"closed":2}` {
		t.Fatalf("cancel: %d %s", rec.Code, rec.Body.String())
	}

	var got db.Epic
	h.DB.First(&got, "id = ?", e.ID)
	if got.State != epics.StateCancelled || got.CancelReason != "scrapped" || got.CancelledBy != "leon" {
		t.Fatalf("epic: state=%q reason=%q by=%q", got.State, got.CancelReason, got.CancelledBy)
	}
	for _, id := range []string{a.ID, b.ID} {
		var kid db.Ticket
		h.DB.First(&kid, "id = ?", id)
		if kid.Phase != "closed" || kid.Branch == "" {
			t.Errorf("child %s: phase=%q branch=%q", id, kid.Phase, kid.Branch)
		}
		var entry db.LogEntry
		if err := h.DB.Where("ticket_id = ? AND entry_type = ?", id, "STATUS").Last(&entry).Error; err != nil || !strings.Contains(entry.Message, "scrapped") {
			t.Errorf("child %s log: %q %v", id, entry.Message, err)
		}
	}
	var open int64
	h.DB.Model(&db.HumanInput{}).Where("ticket_id = ?", b.ID).Count(&open)
	if open != 0 {
		t.Error("open questions must be dropped with the close")
	}
	var closes int64
	h.DB.Model(&db.GitHubOutbox{}).Where("ticket_id = ? AND kind = ?", b.ID, ghsync.KindClose).Count(&closes)
	if closes != 1 {
		t.Errorf("issue close rows = %d, want 1", closes)
	}

	client.SetReadDeadline(time.Now().Add(2 * time.Second))
	var msg ws.WSMessage
	if err := client.ReadJSON(&msg); err != nil || msg.Type != "ticket_closed" || msg.TicketID == nil || *msg.TicketID != a.ID {
		t.Fatalf("shem push: %+v %v", msg, err)
	}

	rec = epicRequest(t, mux, cookie, "POST", "/api/epics/"+e.ID+"/cancel", nil)
	if rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != `{"closed":0}` {
		t.Fatalf("repeat cancel: %d %s", rec.Code, rec.Body.String())
	}
	rec = epicRequest(t, mux, cookie, "POST", "/api/epics/"+e.ID+"/resume", nil)
	if rec.Code != http.StatusConflict {
		t.Fatalf("resume after cancel: %d", rec.Code)
	}
}

// The body is optional, a form-encoded reason works for the page's button,
// and completed or unknown runs are refused.
func TestCancelEpicBodiesAndRefusals(t *testing.T) {
	h, mux, cookie := setupEpicTest(t)
	e := createTestEpic(t, mux, cookie)

	req := httptest.NewRequest("POST", "/api/epics/"+e.ID+"/cancel", strings.NewReader("reason=dup"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(cookie)
	req.Header.Set("X-CSRF-Token", auth.CSRFTokenForSession(cookie.Value))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("form cancel: %d %s", rec.Code, rec.Body.String())
	}
	var got db.Epic
	h.DB.First(&got, "id = ?", e.ID)
	if got.State != epics.StateCancelled || got.CancelReason != "dup" {
		t.Fatalf("form cancel: state=%q reason=%q", got.State, got.CancelReason)
	}

	done := createTestEpic(t, mux, cookie)
	h.DB.Model(&db.Epic{}).Where("id = ?", done.ID).Update("state", epics.StateCompleted)
	if rec := epicRequest(t, mux, cookie, "POST", "/api/epics/"+done.ID+"/cancel", nil); rec.Code != http.StatusConflict {
		t.Fatalf("cancel completed: %d", rec.Code)
	}
	if rec := epicRequest(t, mux, cookie, "POST", "/api/epics/nope/cancel", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("cancel missing: %d", rec.Code)
	}
	if rec := epicRequest(t, mux, cookie, "POST", "/api/epics/"+e.ID+"/cancel", "not an object"); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad body: %d", rec.Code)
	}

	var list []db.Epic
	json.Unmarshal(epicRequest(t, mux, cookie, "GET", "/api/epics?state=cancelled", nil).Body.Bytes(), &list)
	if len(list) != 1 || list[0].ID != e.ID {
		t.Fatalf("cancelled filter: %+v", list)
	}
}
