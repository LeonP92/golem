package api_test

import (
	"bytes"
	"encoding/json"
	"errors"

	"gorm.io/gorm"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/leonp92/golem/internal/orchestrator/api"
	"github.com/leonp92/golem/internal/orchestrator/db"
	"github.com/leonp92/golem/internal/shem/client"
)

type recoveryHarness struct {
	h      *api.Handlers
	mux    *http.ServeMux
	shem   db.Shem
	ticket db.Ticket
}

func newRecoveryHarness(t *testing.T) recoveryHarness {
	t.Helper()
	h, mux := setupTicketTest(t)
	shem := seedShem(t, h, "recover", "recover-key")
	phase, sha := "implement", strings.Repeat("a", 40)
	ticket := db.Ticket{RepoRemote: "local://repo", Branch: "ticket/test", Description: "fix test", Phase: "implement", AssignedShem: &shem.ID, CheckpointPhase: &phase, CheckpointSHA: &sha}
	if err := h.DB.Create(&ticket).Error; err != nil {
		t.Fatal(err)
	}
	return recoveryHarness{h, mux, shem, ticket}
}
func (f recoveryHarness) request(t *testing.T, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	data, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(method, path, bytes.NewReader(data))
	req.Header.Set("Authorization", "Bearer recover-key")
	req.Header.Set("X-Shem-Name", "recover")
	out := httptest.NewRecorder()
	f.mux.ServeHTTP(out, req)
	return out
}
func (f recoveryHarness) fail(t *testing.T, reason string) {
	t.Helper()
	out := f.request(t, "PATCH", "/api/tickets/"+f.ticket.ID+"/phase", map[string]string{"phase": "needs-attention", "attention_reason": reason})
	if out.Code != 204 {
		t.Fatalf("failure: %d %s", out.Code, out.Body.String())
	}
}
func (f recoveryHarness) claim(t *testing.T, max int) client.AttentionClaim {
	t.Helper()
	out := f.request(t, "POST", "/api/tickets/"+f.ticket.ID+"/attention-claim", map[string]int{"max_attempts": max})
	if out.Code != 200 {
		t.Fatalf("claim: %d %s", out.Code, out.Body.String())
	}
	var claim client.AttentionClaim
	if err := json.Unmarshal(out.Body.Bytes(), &claim); err != nil {
		t.Fatal(err)
	}
	return claim
}
func (f recoveryHarness) resolve(t *testing.T, c client.AttentionClaim) *httptest.ResponseRecorder {
	return f.request(t, "POST", "/api/tickets/"+f.ticket.ID+"/attention-result", client.AttentionResult{Token: c.Token, Retry: true, Reason: "test assertion wrong", Feedback: "fix handler to return 200"})
}

func TestAttentionFeedbackRetryAndSameFailureStop(t *testing.T) {
	f := newRecoveryHarness(t)
	f.fail(t, "test returns 500, expected 200")
	c := f.claim(t, 3)
	if c.Attempt != 1 || c.Claim.RetryPhase != "implement" {
		t.Fatalf("claim: %+v", c)
	}
	if out := f.resolve(t, c); out.Code != 200 {
		t.Fatalf("resolve: %d %s", out.Code, out.Body.String())
	}
	var fresh db.Ticket
	f.h.DB.First(&fresh, "id = ?", f.ticket.ID)
	if fresh.Phase != "implement" || fresh.AssignedShem == nil || *fresh.AssignedShem != f.shem.ID {
		t.Fatalf("retry lost owner or phase: %+v", fresh)
	}
	var feedback db.HumanInput
	f.h.DB.Where("ticket_id = ? AND kind = 'feedback'", f.ticket.ID).First(&feedback)
	if feedback.Prompt != "fix handler to return 200" {
		t.Fatalf("feedback: %+v", feedback)
	}
	if out := f.resolve(t, c); out.Code != 409 {
		t.Fatal("duplicate result dispatched twice")
	}
	f.fail(t, "test returns 500, expected 200")
	f.h.DB.First(&fresh, "id = ?", f.ticket.ID)
	if fresh.AttentionEligible || !strings.Contains(fresh.AttentionReason, "Same failure twice") {
		t.Fatalf("not stopped: %+v", fresh)
	}
}

func TestAttentionCapPersistsAcrossChangingFailures(t *testing.T) {
	f := newRecoveryHarness(t)
	for i, reason := range []string{"compiler error", "failing test", "review finding"} {
		f.fail(t, reason)
		c := f.claim(t, 3)
		if c.Attempt != i+1 {
			t.Fatalf("attempt %d", c.Attempt)
		}
		if out := f.resolve(t, c); out.Code != 200 {
			t.Fatal(out.Body.String())
		}
	}
	f.fail(t, "fourth different failure")
	out := f.request(t, "POST", "/api/tickets/"+f.ticket.ID+"/attention-claim", map[string]int{"max_attempts": 3})
	if out.Code != 409 {
		t.Fatal("cap bypassed")
	}
	var fresh db.Ticket
	f.h.DB.First(&fresh, "id = ?", f.ticket.ID)
	if fresh.AttentionAttempts != 3 || fresh.AttentionEligible {
		t.Fatalf("cap: %+v", fresh)
	}
}

func TestAttentionNeverTakesHumanOrLegacyParks(t *testing.T) {
	f := newRecoveryHarness(t)
	f.h.DB.Model(&f.ticket).Updates(db.PhaseUpdates("needs-attention"))
	out := f.request(t, "GET", "/api/tickets?attention=true", nil)
	if out.Code != 200 || strings.TrimSpace(out.Body.String()) != "[]" {
		t.Fatalf("legacy park listed: %s", out.Body.String())
	}
	out = f.request(t, "POST", "/api/tickets/"+f.ticket.ID+"/attention-claim", map[string]int{"max_attempts": 3})
	if out.Code != 409 {
		t.Fatal("human park claimable")
	}
}

func TestAttentionLeaseAndHumanChangesWin(t *testing.T) {
	for _, change := range []string{"stopped", "needs-attention", "unassigned", "edited-issue", "paused-epic", "expired"} {
		t.Run(change, func(t *testing.T) {
			f := newRecoveryHarness(t)
			f.fail(t, "review finding")
			c := f.claim(t, 3)
			again := f.request(t, "POST", "/api/tickets/"+f.ticket.ID+"/attention-claim", map[string]int{"max_attempts": 3})
			if again.Code != 409 {
				t.Fatal("second diagnoser got lease")
			}
			switch change {
			case "edited-issue":
				n := 42
				f.h.DB.Model(&f.ticket).Updates(map[string]any{"issue_number": n, "intake_approved": true, "body_hash": "new", "approved_body_hash": "old"})
			case "paused-epic":
				e := db.Epic{RepoRemote: f.ticket.RepoRemote, State: "paused"}
				if err := f.h.DB.Create(&e).Error; err != nil {
					t.Fatal(err)
				}
				f.h.DB.Model(&f.ticket).Update("epic_id", e.ID)
			case "expired":
				f.h.DB.Model(&f.ticket).Update("attention_lease_until", time.Now().Add(-time.Minute))
			default:
				f.h.DB.Model(&f.ticket).Updates(db.PhaseUpdates(change))
			}
			if out := f.resolve(t, c); out.Code != 409 {
				t.Fatalf("late diagnosis undid %s: %d %s", change, out.Code, out.Body.String())
			}
		})
	}
}

func TestAttentionExpiredLeaseCountsAgainstBudget(t *testing.T) {
	f := newRecoveryHarness(t)
	f.fail(t, "review finding")
	first := f.claim(t, 3)
	f.h.DB.Model(&f.ticket).Update("attention_lease_until", time.Now().Add(-time.Minute))
	second := f.claim(t, 3)
	if second.Attempt != 2 || second.Token == first.Token {
		t.Fatal("lease retry lost budget or fencing")
	}
	if out := f.resolve(t, first); out.Code != 409 {
		t.Fatal("old lease could complete")
	}
}

func TestAttentionRetrySurvivesWorkerPhaseReport(t *testing.T) {
	f := newRecoveryHarness(t)
	f.fail(t, "review finding")
	c := f.claim(t, 3)
	if out := f.resolve(t, c); out.Code != 200 {
		t.Fatal(out.Body.String())
	}
	out := f.request(t, "PATCH", "/api/tickets/"+f.ticket.ID+"/phase", map[string]string{"phase": "implement"})
	if out.Code != 204 {
		t.Fatal(out.Body.String())
	}
	out = f.request(t, "GET", "/api/tickets/resumable", nil)
	var claims []api.ClaimResponse
	if err := json.Unmarshal(out.Body.Bytes(), &claims); err != nil {
		t.Fatal(err)
	}
	if len(claims) != 1 || claims[0].RetryPhase != "implement" {
		t.Fatalf("restart lost retry: %s", out.Body.String())
	}
}

func TestAttentionCannotBeClaimedByAnotherShem(t *testing.T) {
	f := newRecoveryHarness(t)
	f.fail(t, "review finding")
	other := seedShem(t, f.h, "other", "other-key")
	_ = other
	req := httptest.NewRequest("POST", "/api/tickets/"+f.ticket.ID+"/attention-claim", strings.NewReader(`{"max_attempts":3}`))
	req.Header.Set("Authorization", "Bearer other-key")
	req.Header.Set("X-Shem-Name", "other")
	out := httptest.NewRecorder()
	f.mux.ServeHTTP(out, req)
	if out.Code != 409 {
		t.Fatalf("another shem claimed diagnosis: %d", out.Code)
	}
}

// Storage failures are not stale claims. They must remain visible as server
// errors, and a failed transaction must not spend budget or resume the ticket.
func TestAttentionDatabaseErrorsAreNotConflicts(t *testing.T) {
	for _, action := range []string{"claim", "resolve"} {
		for _, operation := range []string{"query", "update"} {
			t.Run(action+"/"+operation, func(t *testing.T) {
				f := newRecoveryHarness(t)
				f.fail(t, "review finding")
				var claim client.AttentionClaim
				attempts := 0
				if action == "resolve" {
					claim = f.claim(t, 3)
					attempts = 1
				}
				fail := func(tx *gorm.DB) {
					if tx.Statement.Table == "tickets" {
						tx.AddError(errors.New("storage unavailable"))
					}
				}
				const hook = "test:attention_storage_failure"
				var remove func() error
				if operation == "query" {
					if err := f.h.DB.Callback().Query().Before("gorm:query").Register(hook, fail); err != nil {
						t.Fatal(err)
					}
					remove = func() error { return f.h.DB.Callback().Query().Remove(hook) }
				} else {
					if err := f.h.DB.Callback().Update().Before("gorm:update").Register(hook, fail); err != nil {
						t.Fatal(err)
					}
					remove = func() error { return f.h.DB.Callback().Update().Remove(hook) }
				}
				t.Cleanup(func() {
					if err := remove(); err != nil {
						t.Error(err)
					}
				})
				var out *httptest.ResponseRecorder
				if action == "claim" {
					out = f.request(t, "POST", "/api/tickets/"+f.ticket.ID+"/attention-claim", map[string]int{"max_attempts": 3})
				} else {
					out = f.resolve(t, claim)
				}
				if out.Code != http.StatusInternalServerError {
					t.Fatalf("storage error returned %d: %s", out.Code, out.Body.String())
				}
				if err := remove(); err != nil {
					t.Fatal(err)
				}
				var fresh db.Ticket
				if err := f.h.DB.First(&fresh, "id = ?", f.ticket.ID).Error; err != nil {
					t.Fatal(err)
				}
				if fresh.Phase != "needs-attention" || fresh.AttentionAttempts != attempts || !fresh.AttentionEligible {
					t.Fatalf("failed transaction changed recovery: %+v", fresh)
				}
				var feedback int64
				f.h.DB.Model(&db.HumanInput{}).Where("ticket_id = ? AND kind = 'feedback'", f.ticket.ID).Count(&feedback)
				if feedback != 0 {
					t.Fatal("failed transaction leaked feedback")
				}
			})
		}
	}
}

// The deprecated alias keeps answering shems built before the read moved onto
// the list endpoint, in its original compact shape.
func TestAttentionAliasStillAnswers(t *testing.T) {
	f := newRecoveryHarness(t)
	f.fail(t, "API Error: 503 overloaded")
	out := f.request(t, "GET", "/api/tickets/attention", nil)
	if out.Code != 200 || !strings.Contains(out.Body.String(), `"ticket_id"`) {
		t.Fatalf("alias: %d %s", out.Code, out.Body.String())
	}
}

// The list endpoint keeps its two readers apart: a shem must narrow its read
// with a filter, the filters are mutually exclusive, and a browser session is
// unaffected by them.
func TestListFiltersGuardShemReads(t *testing.T) {
	f := newRecoveryHarness(t)
	if out := f.request(t, "GET", "/api/tickets", nil); out.Code != 400 {
		t.Fatalf("unfiltered shem list: %d", out.Code)
	}
	if out := f.request(t, "GET", "/api/tickets?attention=true&migratable=true", nil); out.Code != 400 {
		t.Fatalf("both filters: %d", out.Code)
	}
	req := httptest.NewRequest("GET", "/api/tickets?attention=true", http.NoBody)
	out := httptest.NewRecorder()
	f.mux.ServeHTTP(out, req)
	if out.Code != http.StatusFound {
		t.Fatalf("no credentials: %d", out.Code)
	}
}

// X-Shem-Name only selects the auth path; identity still comes from the
// verified key. A crafted header cannot read another shem's filters or row.
func TestListFiltersAuthenticateTheNamedShem(t *testing.T) {
	f := newRecoveryHarness(t)
	seedShem(t, f.h, "other-shem", "other-key")

	// The recover shem's key with another shem's name: no ticket, no row.
	req := httptest.NewRequest("GET", "/api/tickets?attention=true", http.NoBody)
	req.Header.Set("Authorization", "Bearer recover-key")
	req.Header.Set("X-Shem-Name", "other-shem")
	out := httptest.NewRecorder()
	f.mux.ServeHTTP(out, req)
	if out.Code != http.StatusUnauthorized {
		t.Fatalf("wrong-key header accepted: %d", out.Code)
	}

	// A shem header with no key at all is the API-key path and fails closed;
	// it never falls through to the session list.
	req = httptest.NewRequest("GET", "/api/tickets?attention=true", http.NoBody)
	req.Header.Set("X-Shem-Name", "other-shem")
	out = httptest.NewRecorder()
	f.mux.ServeHTTP(out, req)
	if out.Code != http.StatusUnauthorized {
		t.Fatalf("keyless header accepted: %d", out.Code)
	}

	// Each shem's filters see only its own tickets.
	f.fail(t, "API Error: 503 overloaded")
	asOther := func(path string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("GET", path, http.NoBody)
		req.Header.Set("Authorization", "Bearer other-key")
		req.Header.Set("X-Shem-Name", "other-shem")
		rec := httptest.NewRecorder()
		f.mux.ServeHTTP(rec, req)
		return rec
	}
	if out := asOther("/api/tickets?attention=true"); strings.Contains(out.Body.String(), f.ticket.ID) {
		t.Fatalf("attention filter leaked another shem's ticket: %s", out.Body.String())
	}
	if out := asOther("/api/tickets?migratable=true"); strings.Contains(out.Body.String(), f.ticket.ID) {
		t.Fatalf("migratable filter leaked another shem's ticket: %s", out.Body.String())
	}
	if out := asOther("/api/shems/me"); out.Code != 200 || strings.Contains(out.Body.String(), `"recover"`) {
		t.Fatalf("me returned the wrong row: %d %s", out.Code, out.Body.String())
	}
	if got := f.attentionList(t); len(got) != 1 || got[0].TicketID != f.ticket.ID {
		t.Fatalf("owner's filter: %+v", got)
	}

}
