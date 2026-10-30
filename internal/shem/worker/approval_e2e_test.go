package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"

	"github.com/leonp92/golem/internal/orchestrator/api"
	"github.com/leonp92/golem/internal/orchestrator/auth"
	"github.com/leonp92/golem/internal/orchestrator/db"
	"github.com/leonp92/golem/internal/orchestrator/sse"
	ws "github.com/leonp92/golem/internal/orchestrator/ws"
	"github.com/leonp92/golem/internal/shem/client"
)

// approvalStack is a real orchestrator (handlers over HTTP, in-memory SQLite)
// with a human session and a shem client, so the approval gate runs against
// the same endpoints a deployed shem and dashboard use.
type approvalStack struct {
	t      *testing.T
	gdb    *gorm.DB
	srv    *httptest.Server
	cookie *http.Cookie
	shem   *client.Client
}

func newApprovalStack(t *testing.T) *approvalStack {
	t.Helper()
	gdb, err := db.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, _ := gdb.DB()
	sqlDB.SetMaxOpenConns(1)

	h := api.NewHandlers(gdb, ws.NewHub(), sse.NewBroker())
	mux := http.NewServeMux()
	h.RegisterHumanRoutes(mux)
	h.RegisterShemRoutes(mux)
	h.RegisterTicketRoutes(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	user := db.User{Username: "approver", PasswordHash: "x", Role: "admin"}
	if err := gdb.Create(&user).Error; err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	if err := auth.CreateSession(gdb, rec, user.ID, false); err != nil {
		t.Fatal(err)
	}

	hash, _ := bcrypt.GenerateFromPassword([]byte("key"), bcrypt.MinCost)
	shem := db.Shem{Name: "s1", APIKeyHash: string(hash), Repos: `["local://test"]`, Status: "online"}
	if err := gdb.Create(&shem).Error; err != nil {
		t.Fatal(err)
	}
	return &approvalStack{t: t, gdb: gdb, srv: srv, cookie: rec.Result().Cookies()[0],
		shem: client.New(srv.URL, "key", "s1")}
}

// human POSTs as the dashboard does: session cookie plus CSRF header.
func (s *approvalStack) human(method, path string, body any) int {
	s.t.Helper()
	b, _ := json.Marshal(body)
	req, _ := http.NewRequest(method, s.srv.URL+path, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(s.cookie)
	req.Header.Set(auth.CSRFHeader, auth.CSRFTokenForSession(s.cookie.Value))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		s.t.Fatal(err)
	}
	_ = resp.Body.Close()
	return resp.StatusCode
}

// newTicket creates a ticket through the API and claims it as the shem would.
func (s *approvalStack) newTicket(manual bool) *client.ClaimResponse {
	s.t.Helper()
	if code := s.human("POST", "/api/tickets", map[string]any{"repo_remote": "local://test",
		"branch": "main", "title": "t", "description": "d", "manual_approval": manual}); code != http.StatusCreated {
		s.t.Fatalf("create ticket: %d", code)
	}
	var tk db.Ticket
	s.gdb.Order("created_at desc").First(&tk)
	claim, err := s.shem.ClaimTicket(tk.ID)
	if err != nil {
		s.t.Fatal(err)
	}
	if err := s.shem.PostPhase(tk.ID, "brainstorm"); err != nil {
		s.t.Fatal(err)
	}
	return claim
}

func (s *approvalStack) approvals(id string) (n int64) {
	s.gdb.Model(&db.HumanInput{}).Where("ticket_id = ? AND kind = 'approval'", id).Count(&n)
	return n
}

// waitOpen waits until the shem has an approval request open on ticket id.
func (s *approvalStack) waitOpen(id string) {
	s.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		var n int64
		s.gdb.Model(&db.HumanInput{}).
			Where("ticket_id = ? AND kind = 'approval' AND resolved_at IS NULL", id).Count(&n)
		if n > 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	s.t.Fatal("the shem never asked for approval")
}

func (s *approvalStack) phase(id string) string {
	var tk db.Ticket
	s.gdb.First(&tk, "id = ?", id)
	return tk.Phase
}

func TestDefaultTicketsAdvanceWithoutAskingAHuman(t *testing.T) {
	fast(t)
	s := newApprovalStack(t)
	claim := s.newTicket(false)
	if claim.ManualApproval {
		t.Fatal("a ticket created without the box must claim as automatic")
	}
	runs := 0
	out, err := gatedPhase(context.Background(), s.shem, claim.TicketID, claim.ManualApproval, "p", noStatus,
		func() error { runs++; return nil })
	if err != nil || out != approvalGranted || runs != 1 {
		t.Fatalf("out=%v err=%v runs=%d", out, err, runs)
	}
	if n := s.approvals(claim.TicketID); n != 0 {
		t.Fatalf("automatic mode created %d approval requests", n)
	}
}

func TestManualTicketsWaitForAHumanAndRerunOnRequestedChanges(t *testing.T) {
	fast(t)
	s := newApprovalStack(t)
	claim := s.newTicket(true)
	id := claim.TicketID
	if !claim.ManualApproval {
		t.Fatal("a ticket created with the box ticked must claim as manual")
	}

	var mu sync.Mutex
	var feedback []string
	run := func() error {
		mu.Lock()
		defer mu.Unlock()
		feedback = append(feedback, consumeFeedback(context.Background(), s.shem, id))
		return nil
	}
	type result struct {
		out approvalOutcome
		err error
	}
	done := make(chan result, 1)
	go func() {
		out, err := gatedPhase(context.Background(), s.shem, id, true, "approve the spec", noStatus, run)
		done <- result{out, err}
	}()

	// The shem stops and waits; nothing advances without a human.
	s.waitOpen(id)
	select {
	case r := <-done:
		t.Fatalf("gate opened with no human answer: %+v", r)
	case <-time.After(100 * time.Millisecond):
	}

	// Request changes: the phase reruns with the feedback, then asks again.
	if code := s.human("POST", "/api/tickets/"+id+"/actions",
		map[string]string{"action": "request-changes", "feedback": "tighten the scope"}); code != http.StatusNoContent {
		t.Fatalf("request-changes: %d", code)
	}
	deadline := time.Now().Add(10 * time.Second)
	for s.approvals(id) < 2 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	s.waitOpen(id)

	// Approve: the gate opens and the dashboard's approve advances the phase.
	if code := s.human("POST", "/api/tickets/"+id+"/actions", map[string]string{"action": "approve"}); code != http.StatusNoContent && code != http.StatusOK {
		t.Fatalf("approve: %d", code)
	}
	select {
	case r := <-done:
		if r.err != nil || r.out != approvalGranted {
			t.Fatalf("out=%v err=%v", r.out, r.err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("approval never reached the shem")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(feedback) != 2 || feedback[0] != "" || feedback[1] != "tighten the scope" {
		t.Fatalf("runs saw feedback %q, want one clean run and one with the requested changes", feedback)
	}
	if got := s.phase(id); got != "plan" {
		t.Fatalf("phase after approving the spec = %q, want plan", got)
	}
}

func TestARestartedShemWaitsOnTheOpenRequestInsteadOfRerunning(t *testing.T) {
	fast(t)
	s := newApprovalStack(t)
	id := s.newTicket(true).TicketID

	// First shem: runs the phase, asks, then dies while waiting.
	ctx, crash := context.WithCancel(context.Background())
	var runs atomic.Int32
	first := make(chan struct{})
	go func() {
		_, _ = gatedPhase(ctx, s.shem, id, true, "p", noStatus, func() error { runs.Add(1); return nil })
		close(first)
	}()
	s.waitOpen(id)
	crash()
	<-first

	// Second shem: finds the open request and waits on it.
	done := make(chan approvalOutcome, 1)
	go func() {
		out, _ := gatedPhase(context.Background(), s.shem, id, true, "p", noStatus, func() error { runs.Add(1); return nil })
		done <- out
	}()
	time.Sleep(50 * time.Millisecond)
	s.human("POST", "/api/tickets/"+id+"/actions", map[string]string{"action": "approve"})
	select {
	case out := <-done:
		if out != approvalGranted || runs.Load() != 1 || s.approvals(id) != 1 {
			t.Fatalf("out=%v runs=%d approvals=%d", out, runs.Load(), s.approvals(id))
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the restarted shem never saw the approval")
	}
}
