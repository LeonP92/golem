package api_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/leonp92/golem/internal/orchestrator/db"
	"github.com/leonp92/golem/internal/shem/client"
)

func (f recoveryHarness) resolveResult(t *testing.T, c client.AttentionClaim, body *client.AttentionResult) *httptest.ResponseRecorder {
	r := body
	if r == nil {
		r = &client.AttentionResult{Token: c.Token, Retry: true, Reason: "test assertion wrong", Feedback: "fix handler to return 200"}
	}
	if r.Token == "" {
		r.Token = c.Token
	}
	return f.request(t, "POST", "/api/tickets/"+f.ticket.ID+"/attention-result", r)
}

func (f recoveryHarness) attentionList(t *testing.T) []client.AttentionTicket {
	out := f.request(t, "GET", "/api/tickets?attention=true", nil)
	if out.Code != 200 {
		t.Fatalf("list: %d", out.Code)
	}
	var listed []client.AttentionTicket
	if err := json.Unmarshal(out.Body.Bytes(), &listed); err != nil {
		t.Fatal(err)
	}
	return listed
}

func (f recoveryHarness) fresh(t *testing.T) db.Ticket {
	var fresh db.Ticket
	if err := f.h.DB.First(&fresh, "id = ?", f.ticket.ID).Error; err != nil {
		t.Fatal(err)
	}
	return fresh
}

func (f recoveryHarness) claimCode(t *testing.T, max int) int {
	out := f.request(t, "POST", "/api/tickets/"+f.ticket.ID+"/attention-claim", map[string]int{"max_attempts": max})
	return out.Code
}

func TestSupervisorWaitCycle(t *testing.T) {
	f := newRecoveryHarness(t)
	f.fail(t, "API Error: 503 overloaded")
	c := f.claim(t, 3)
	if c.Attempt != 1 || c.WaitResume {
		t.Fatalf("fresh claim: %+v", c)
	}
	out := f.resolveResult(t, c, &client.AttentionResult{Wait: "transient", Reason: "API Error: 503 overloaded"})
	if out.Code != 204 {
		t.Fatalf("wait: %d %s", out.Code, out.Body.String())
	}
	fresh := f.fresh(t)
	if fresh.AttentionWaitUntil == nil || fresh.AttentionWaitChecks != 1 || fresh.AttentionWaitDeadline == nil || fresh.AttentionWaitFingerprint == "" {
		t.Fatalf("no wait incident: %+v", fresh)
	}
	if !fresh.AttentionEligible || fresh.Phase != "needs-attention" {
		t.Fatalf("wait broke eligibility: %+v", fresh)
	}
	// A waiting ticket is hidden from dispatch and from claims until due.
	if n := len(f.attentionList(t)); n != 0 {
		t.Fatalf("waiting ticket listed: %d", n)
	}
	if code := f.claimCode(t, 3); code != 409 {
		t.Fatalf("waiting ticket claimed: %d", code)
	}
	// Once due, the claim resumes the wait; the fault never spent an attempt.
	f.h.DB.Model(&db.Ticket{}).Where("id = ?", f.ticket.ID).Update("attention_wait_until", time.Now().Add(-time.Minute))
	c = f.claim(t, 3)
	if !c.WaitResume || c.Attempt != 0 {
		t.Fatalf("wait-resume claim: %+v", c)
	}
	out = f.resolveResult(t, c, &client.AttentionResult{Retry: true, Reason: "waited", Feedback: "retry the saved step"})
	if out.Code != 200 {
		t.Fatalf("resume: %d %s", out.Code, out.Body.String())
	}
	fresh = f.fresh(t)
	if fresh.Phase != "implement" || fresh.AttentionWaitUntil != nil || fresh.AttentionWaitFingerprint == "" {
		t.Fatalf("resume lost incident or phase: %+v", fresh)
	}
	// A repeat of the same transient fault continues the wait: no
	// same-failure-twice stop, no new attempt spent.
	f.fail(t, "API Error: 503 overloaded")
	fresh = f.fresh(t)
	if !fresh.AttentionEligible || strings.Contains(fresh.AttentionReason, "Same failure twice") {
		t.Fatalf("wait repeat stopped: %+v", fresh)
	}
	c = f.claim(t, 3)
	if c.WaitResume || c.Attempt != 0 {
		t.Fatalf("wait-continuation claim: %+v", c)
	}
	out = f.resolveResult(t, c, &client.AttentionResult{Wait: "transient", Reason: "API Error: 503 overloaded"})
	if out.Code != 204 {
		t.Fatalf("second wait: %d", out.Code)
	}
	if fresh = f.fresh(t); fresh.AttentionWaitChecks != 2 {
		t.Fatalf("checks: %d", fresh.AttentionWaitChecks)
	}
}

func TestSupervisorWaitBudgetExhausts(t *testing.T) {
	f := newRecoveryHarness(t)
	f.fail(t, "connection reset by peer")
	f.h.DB.Model(&db.Ticket{}).Where("id = ?", f.ticket.ID).Updates(map[string]any{
		"attention_wait_checks": 5, "attention_wait_fingerprint": f.fresh(t).AttentionFingerprint,
		"attention_wait_deadline": time.Now().Add(10 * time.Minute)})
	c := f.claim(t, 3)
	if out := f.resolveResult(t, c, &client.AttentionResult{Wait: "transient", Reason: "connection reset by peer"}); out.Code != 409 {
		t.Fatalf("wait past budget: %d", out.Code)
	}
	// The refused wait leaves the dispatch open for the escalation.
	out := f.resolveResult(t, c, &client.AttentionResult{Reason: "Transient fault persisted beyond the wait budget"})
	if out.Code != 204 {
		t.Fatalf("escalation: %d", out.Code)
	}
	if fresh := f.fresh(t); fresh.AttentionEligible || fresh.Phase != "needs-attention" {
		t.Fatalf("not parked: %+v", fresh)
	}
}

func TestSupervisorWaitStaleReasonRefused(t *testing.T) {
	f := newRecoveryHarness(t)
	f.fail(t, "API Error: 503 overloaded")
	c := f.claim(t, 3)
	// A wait naming a different failure than the ticket's current one is stale.
	if out := f.resolveResult(t, c, &client.AttentionResult{Wait: "transient", Reason: "timeout waiting for model"}); out.Code != 409 {
		t.Fatalf("stale wait: %d", out.Code)
	}
}

func TestSupervisorManualTicketNeverRecovered(t *testing.T) {
	f := newRecoveryHarness(t)
	f.h.DB.Model(&db.Ticket{}).Where("id = ?", f.ticket.ID).Update("manual_approval", true)
	f.fail(t, "test returns 500, expected 200")
	if n := len(f.attentionList(t)); n != 0 {
		t.Fatalf("manual ticket listed for recovery: %d", n)
	}
	if code := f.claimCode(t, 3); code != 409 {
		t.Fatalf("manual ticket claimed: %d", code)
	}
	out := f.request(t, "POST", "/api/tickets/"+f.ticket.ID+"/attention-result",
		client.AttentionResult{Token: "anything", Retry: true, Reason: "x", Feedback: "y"})
	if out.Code != 409 {
		t.Fatalf("manual ticket resolved: %d", out.Code)
	}
	fresh := f.fresh(t)
	if fresh.Phase != "needs-attention" || !fresh.AttentionEligible {
		t.Fatalf("manual park disturbed: %+v", fresh)
	}
}

func TestSupervisorDecisionCommitsWithResume(t *testing.T) {
	f := newRecoveryHarness(t)
	f.fail(t, "reviewer failed: spec conflict")
	c := f.claim(t, 3)
	out := f.resolveResult(t, c, &client.AttentionResult{
		Retry: true, Reason: "spec conflict", Feedback: "use B",
		Decision: &client.AttentionDecision{Question: "q", Choice: "B", Reason: "r", Basis: "spec 3"}})
	if out.Code != 200 {
		t.Fatalf("decide resolve: %d", out.Code)
	}
	fresh := f.fresh(t)
	if fresh.Phase != "implement" {
		t.Fatalf("not resumed: %s", fresh.Phase)
	}
	var decisions int64
	f.h.DB.Model(&db.LogEntry{}).Where("ticket_id = ? AND entry_type = 'DECISION'", f.ticket.ID).Count(&decisions)
	if decisions != 1 {
		t.Fatalf("DECISION entries: %d", decisions)
	}
	// An incomplete decision is rejected before anything commits.
	f.fail(t, "reviewer failed: another conflict")
	c = f.claim(t, 3)
	out = f.resolveResult(t, c, &client.AttentionResult{
		Retry: true, Reason: "x", Feedback: "y",
		Decision: &client.AttentionDecision{Question: "q"}})
	if out.Code != 400 {
		t.Fatalf("incomplete decision: %d", out.Code)
	}
	if fresh = f.fresh(t); fresh.Phase != "needs-attention" {
		t.Fatalf("rejected resolve still resumed: %s", fresh.Phase)
	}
}

func TestSupervisorRejectsMixedOutcomes(t *testing.T) {
	f := newRecoveryHarness(t)
	f.fail(t, "test returns 500")
	c := f.claim(t, 3)
	out := f.resolveResult(t, c, &client.AttentionResult{Wait: "transient", Retry: true, Reason: "x", Feedback: "y"})
	if out.Code != 400 {
		t.Fatalf("mixed outcome: %d", out.Code)
	}
}

func TestWaitIncidentClaimsSkipAttemptCap(t *testing.T) {
	f := newRecoveryHarness(t)
	f.fail(t, "API Error: 529 overloaded")
	c := f.claim(t, 3)
	if out := f.resolveResult(t, c, &client.AttentionResult{Wait: "transient", Reason: "API Error: 529 overloaded"}); out.Code != 204 {
		t.Fatalf("wait: %d", out.Code)
	}
	// Spend the whole diagnosis budget elsewhere, then let the wait come due:
	// the resume must still be claimable, because waits do not burn repair
	// dispatches.
	f.h.DB.Model(&db.Ticket{}).Where("id = ?", f.ticket.ID).Updates(map[string]any{
		"attention_attempts": 3, "attention_wait_until": time.Now().Add(-time.Minute)})
	if code := f.claimCode(t, 3); code != 200 {
		t.Fatalf("wait-resume claim blocked by spent budget: %d", code)
	}
}

// limitWait claims the failed ticket and resolves it as a usage-limit wait.
func (f recoveryHarness) limitWait(t *testing.T, reason string, reset *time.Time) int {
	t.Helper()
	c := f.claim(t, 3)
	return f.resolveResult(t, c, &client.AttentionResult{Wait: "usage_limit", ResetAt: reset, Reason: reason}).Code
}

// advance moves a ticket's wait into the past by d, standing in for elapsed time.
func (f recoveryHarness) advance(t *testing.T, d time.Duration) {
	t.Helper()
	fresh := f.fresh(t)
	f.h.DB.Model(&db.Ticket{}).Where("id = ?", f.ticket.ID).Updates(map[string]any{
		"attention_wait_until":    fresh.AttentionWaitUntil.Add(-d),
		"attention_wait_deadline": fresh.AttentionWaitDeadline.Add(-d)})
}

func TestUsageLimitWaitsUntilResetWithJitter(t *testing.T) {
	f := newRecoveryHarness(t)
	const msg = "ERROR: You've hit your usage limit. try again at 8:00 PM."
	f.fail(t, msg)
	reset := time.Now().Add(3 * time.Hour)
	if code := f.limitWait(t, msg, &reset); code != 204 {
		t.Fatalf("limit wait: %d", code)
	}
	fresh := f.fresh(t)
	off := fresh.AttentionWaitUntil.Sub(reset)
	if off < time.Minute || off > 2*time.Minute || fresh.AttentionWaitKind != "usage_limit" || fresh.AttentionAttempts != 0 {
		t.Fatalf("wait %v after reset, kind %q, attempts %d", off, fresh.AttentionWaitKind, fresh.AttentionAttempts)
	}
	// The same limit repeating after the resume continues the incident.
	f.advance(t, 4*time.Hour)
	c := f.claim(t, 3)
	if !c.WaitResume || c.Attempt != 0 {
		t.Fatalf("resume claim: %+v", c)
	}
	if out := f.resolveResult(t, c, nil); out.Code != 200 {
		t.Fatalf("resume: %d", out.Code)
	}
	f.fail(t, msg)
	if fresh = f.fresh(t); !fresh.AttentionEligible || strings.Contains(fresh.AttentionReason, "Same failure twice") {
		t.Fatalf("repeat limit stopped: %+v", fresh)
	}
}

func TestUsageLimitResetIsCapped(t *testing.T) {
	f := newRecoveryHarness(t)
	f.h.Wait.UsageLimitMax = time.Hour
	f.fail(t, "You've hit your session limit")
	reset := time.Now().Add(20 * time.Hour)
	if code := f.limitWait(t, "You've hit your session limit", &reset); code != 204 {
		t.Fatalf("limit wait: %d", code)
	}
	if until := f.fresh(t).AttentionWaitUntil; until.After(time.Now().Add(time.Hour + time.Second)) {
		t.Fatalf("wait not capped: %v", until)
	}
}

func TestUnparsedLimitBacksOffThenEscalates(t *testing.T) {
	f := newRecoveryHarness(t)
	const msg = "You've hit your usage limit, somehow"
	f.fail(t, msg)
	var got []time.Duration
	for range 20 {
		if code := f.limitWait(t, msg, nil); code != 204 {
			break
		}
		fresh := f.fresh(t)
		got = append(got, time.Until(*fresh.AttentionWaitUntil).Round(time.Minute))
		f.advance(t, time.Until(*fresh.AttentionWaitUntil)+time.Second)
		// The resumed step fails again with the same limit.
		c := f.claim(t, 3)
		f.resolveResult(t, c, nil)
		f.fail(t, msg)
		if !f.fresh(t).AttentionEligible {
			break
		}
	}
	want := []time.Duration{15 * time.Minute, 30 * time.Minute, time.Hour, 2 * time.Hour, 2 * time.Hour}
	if len(got) < len(want) {
		t.Fatalf("escalated early: %v", got)
	}
	for i, w := range want {
		if got[i] != w && (i != len(want)-1 || got[i] > w) {
			t.Fatalf("step %d = %v, want %v (%v)", i, got[i], w, got)
		}
	}
	var total time.Duration
	for _, d := range got {
		total += d
	}
	if total > 6*time.Hour+time.Minute || f.fresh(t).AttentionEligible {
		t.Fatalf("total %v, eligible %v", total, f.fresh(t).AttentionEligible)
	}
}

func TestShemLimitSurvivesHumanRetryAndIsPerShem(t *testing.T) {
	f := newRecoveryHarness(t)
	get := func() string { return strings.TrimSpace(f.request(t, "GET", "/api/shems/me", nil).Body.String()) }
	if got := get(); strings.Contains(got, "limited_until") {
		t.Fatalf("limited without a limit: %s", got)
	}
	const msg = "You've hit your session limit"
	f.fail(t, msg)
	reset := time.Now().Add(2 * time.Hour)
	if code := f.limitWait(t, msg, &reset); code != 204 {
		t.Fatalf("limit wait: %d", code)
	}
	if !strings.Contains(get(), "limited_until") {
		t.Fatalf("limit not recorded on the shem: %s", get())
	}
	// What a human retry or requeue does: the wait is deleted and the ticket
	// leaves the shem. The shem stays limited.
	f.h.DB.Model(&db.Ticket{}).Where("id = ?", f.ticket.ID).
		Updates(map[string]any{"attention_wait_until": nil, "attention_wait_kind": "", "assigned_shem": nil, "phase": "unassigned"})
	if !strings.Contains(get(), "limited_until") {
		t.Fatalf("limit lost when the wait was deleted: %s", get())
	}
	// A sibling shem on the same backend is not assumed to share the account.
	other := seedShem(t, f.h, "sibling", "sibling-key")
	f.h.DB.Model(&db.Shem{}).Where("id IN ?", []uint{f.shem.ID, other.ID}).Update("backend", "codex")
	req := httptest.NewRequest("GET", "/api/shems/me", http.NoBody)
	req.Header.Set("Authorization", "Bearer sibling-key")
	req.Header.Set("X-Shem-Name", "sibling")
	out := httptest.NewRecorder()
	f.mux.ServeHTTP(out, req)
	if strings.Contains(out.Body.String(), "limited_until") {
		t.Fatalf("sibling shem held by another shem's limit: %s", out.Body.String())
	}
}

// The server itself refuses work to a limited shem, whatever a stale or
// restarted client believes, and the hold ends with the clock.
func TestServerRefusesClaimsWhileShemLimited(t *testing.T) {
	f := newRecoveryHarness(t)
	u := db.Ticket{ID: "free-1", RepoRemote: "local://repo", Branch: "ticket/free", Description: "next", Phase: "unassigned"}
	if err := f.h.DB.Create(&u).Error; err != nil {
		t.Fatal(err)
	}
	until := time.Now().Add(time.Hour)
	f.h.DB.Model(&db.Shem{}).Where("id = ?", f.shem.ID).Update("limited_until", until)

	if out := f.request(t, "POST", "/api/tickets/free-1/claim", nil); out.Code != http.StatusConflict || !strings.Contains(out.Body.String(), "usage limit") {
		t.Fatalf("limited shem claimed: %d %s", out.Code, out.Body.String())
	}
	if out := f.request(t, "GET", "/api/tickets/available", nil); strings.TrimSpace(out.Body.String()) != "[]" {
		t.Fatalf("limited shem offered work: %s", out.Body.String())
	}
	var got db.Ticket
	f.h.DB.First(&got, "id = ?", "free-1")
	if got.Phase != "unassigned" || got.AssignedShem != nil {
		t.Fatalf("ticket moved: %+v", got)
	}
	// Expired by the clock alone: claimable again, status gone.
	past := time.Now().Add(-time.Minute)
	f.h.DB.Model(&db.Shem{}).Where("id = ?", f.shem.ID).Update("limited_until", past)
	var row db.Shem
	f.h.DB.First(&row, f.shem.ID)
	if row.UsageLimit() != nil {
		t.Fatal("expired limit still reported")
	}
	if out := f.request(t, "POST", "/api/tickets/free-1/claim", nil); out.Code != http.StatusOK {
		t.Fatalf("claim after the limit: %d %s", out.Code, out.Body.String())
	}
}

func (f recoveryHarness) as(t *testing.T, key, name, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	data, _ := json.Marshal(body)
	req := httptest.NewRequest(method, path, bytes.NewReader(data))
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("X-Shem-Name", name)
	out := httptest.NewRecorder()
	f.mux.ServeHTTP(out, req)
	return out
}

// waitingOnLimit puts the harness ticket where a usage-limit wait leaves it.
func (f recoveryHarness) waitingOnLimit(t *testing.T, extra map[string]any) string {
	t.Helper()
	until := time.Now().Add(2 * time.Hour)
	updates := map[string]any{"phase": "needs-attention", "retry_phase": "implement", "attention_wait_kind": "usage_limit",
		"attention_wait_until": until, "attention_attempts": 2, "attention_token": "stale-token"}
	for k, v := range extra {
		updates[k] = v
	}
	f.h.DB.Model(&db.Ticket{}).Where("id = ?", f.ticket.ID).Updates(updates)
	f.h.DB.Model(&db.Shem{}).Where("id = ?", f.shem.ID).Updates(map[string]any{"backend": "codex", "limited_until": until})
	return *f.ticket.CheckpointSHA
}

func TestMigrateMovesWaitingTicketToUnlimitedSameBackendShem(t *testing.T) {
	f := newRecoveryHarness(t)
	sha := f.waitingOnLimit(t, nil)
	other := seedShem(t, f.h, "other", "other-key")
	f.h.DB.Model(&db.Shem{}).Where("id = ?", other.ID).Update("backend", "codex")
	third := seedShem(t, f.h, "claude", "claude-key")
	f.h.DB.Model(&db.Shem{}).Where("id = ?", third.ID).Update("backend", "claude-code")

	if out := f.request(t, "GET", "/api/tickets?migratable=true", nil); !strings.Contains(out.Body.String(), sha) {
		t.Fatalf("not listed as migratable: %s", out.Body.String())
	}
	// Without the filter the waiting ticket is not listed for recovery.
	if out := f.request(t, "GET", "/api/tickets?attention=true", nil); strings.Contains(out.Body.String(), f.ticket.ID) {
		t.Fatalf("waiting ticket listed without the filter: %s", out.Body.String())
	}
	if out := f.request(t, "POST", "/api/tickets/"+f.ticket.ID+"/migrate", map[string]string{"checkpoint_sha": strings.Repeat("b", 40)}); out.Code != 409 {
		t.Fatalf("a different checkpoint was accepted: %d", out.Code)
	}
	if out := f.request(t, "POST", "/api/tickets/"+f.ticket.ID+"/migrate", map[string]string{"checkpoint_sha": sha}); out.Code != 204 {
		t.Fatalf("migrate: %d %s", out.Code, out.Body.String())
	}
	var got db.Ticket
	f.h.DB.First(&got, "id = ?", f.ticket.ID)
	if got.Phase != "unassigned" || got.AssignedShem != nil || got.RetryPhase != "implement" || got.AttentionAttempts != 2 ||
		got.CheckpointSHA == nil || *got.CheckpointSHA != sha || got.MigratedBackend != "codex" || got.Migrations != 1 ||
		got.AttentionWaitKind != "" || got.AttentionToken != "" {
		t.Fatalf("migrated row wrong: %+v", got)
	}
	// The old owner is fenced: it neither lists nor re-migrates the ticket.
	if out := f.request(t, "POST", "/api/tickets/"+f.ticket.ID+"/migrate", map[string]string{"checkpoint_sha": sha}); out.Code != 409 {
		t.Fatalf("stale owner migrated again: %d", out.Code)
	}
	// A shem of another backend cannot take it; a same-backend shem can.
	if out := f.as(t, "claude-key", "claude", "POST", "/api/tickets/"+f.ticket.ID+"/claim", nil); out.Code != 409 {
		t.Fatalf("another provider claimed a migrated ticket: %d", out.Code)
	}
	if out := f.as(t, "other-key", "other", "GET", "/api/tickets/available", nil); !strings.Contains(out.Body.String(), f.ticket.ID) {
		t.Fatalf("same-backend shem not offered it: %s", out.Body.String())
	}
	if out := f.as(t, "other-key", "other", "POST", "/api/tickets/"+f.ticket.ID+"/claim", nil); out.Code != 200 {
		t.Fatalf("same-backend claim: %d %s", out.Code, out.Body.String())
	}
}

func TestMigrateRefusesIneligibleTickets(t *testing.T) {
	for name, extra := range map[string]map[string]any{
		"manual approval":  {"manual_approval": true},
		"pinned":           {"model_backend": "codex"},
		"already moved":    {"migrations": 1},
		"transient wait":   {"attention_wait_kind": "transient"},
		"no checkpoint":    {"checkpoint_sha": nil},
		"stopped":          {"phase": "stopped"},
		"retried by human": {"phase": "unassigned", "assigned_shem": nil},
	} {
		t.Run(name, func(t *testing.T) {
			f := newRecoveryHarness(t)
			sha := f.waitingOnLimit(t, extra)
			if out := f.request(t, "GET", "/api/tickets?migratable=true", nil); strings.Contains(out.Body.String(), f.ticket.ID) {
				t.Fatalf("listed: %s", out.Body.String())
			}
			if out := f.request(t, "POST", "/api/tickets/"+f.ticket.ID+"/migrate", map[string]string{"checkpoint_sha": sha}); out.Code != 409 {
				t.Fatalf("migrated: %d", out.Code)
			}
			var got db.Ticket
			f.h.DB.First(&got, "id = ?", f.ticket.ID)
			if got.MigratedBackend != "" {
				t.Fatalf("ticket was migrated: %+v", got)
			}
		})
	}
}

// With every same-backend shem limited the ticket waits in the pool, and the
// origin takes it back once its own limit ends: it is never stranded.
func TestMigratedTicketIsNotStrandedWhenNoTargetIsFree(t *testing.T) {
	f := newRecoveryHarness(t)
	sha := f.waitingOnLimit(t, nil)
	other := seedShem(t, f.h, "other", "other-key")
	f.h.DB.Model(&db.Shem{}).Where("id = ?", other.ID).Updates(map[string]any{"backend": "codex", "limited_until": time.Now().Add(time.Hour)})
	if out := f.request(t, "POST", "/api/tickets/"+f.ticket.ID+"/migrate", map[string]string{"checkpoint_sha": sha}); out.Code != 204 {
		t.Fatalf("migrate: %d", out.Code)
	}
	for _, who := range [][2]string{{"recover-key", "recover"}, {"other-key", "other"}} {
		if out := f.as(t, who[0], who[1], "GET", "/api/tickets/available", nil); strings.TrimSpace(out.Body.String()) != "[]" {
			t.Fatalf("%s offered work while limited: %s", who[1], out.Body.String())
		}
	}
	f.h.DB.Model(&db.Shem{}).Where("id = ?", f.shem.ID).Update("limited_until", time.Now().Add(-time.Minute))
	if out := f.request(t, "POST", "/api/tickets/"+f.ticket.ID+"/claim", nil); out.Code != 200 {
		t.Fatalf("origin could not take its ticket back: %d %s", out.Code, out.Body.String())
	}
}

// A shem that pushed a capture commit names it; the ticket then resumes from it.
func TestMigrateRecordsTheCapturedCheckpoint(t *testing.T) {
	f := newRecoveryHarness(t)
	sha := f.waitingOnLimit(t, nil)
	captured := strings.Repeat("c", 40)
	if out := f.request(t, "POST", "/api/tickets/"+f.ticket.ID+"/migrate", map[string]string{"checkpoint_sha": sha, "captured_sha": captured}); out.Code != 204 {
		t.Fatalf("migrate: %d %s", out.Code, out.Body.String())
	}
	var got db.Ticket
	f.h.DB.First(&got, "id = ?", f.ticket.ID)
	if got.CheckpointSHA == nil || *got.CheckpointSHA != captured || got.RetryPhase != "implement" {
		t.Fatalf("resume point wrong: %+v", got)
	}
}

// A genuine code failure spends the budget; a waited-out fault never does, from
// zero or from a real earlier repair attempt, across repeated limits.
func TestAttemptsOnlyCountRealFailures(t *testing.T) {
	for _, start := range []int{0, 2} {
		f := newRecoveryHarness(t)
		f.h.DB.Model(&db.Ticket{}).Where("id = ?", f.ticket.ID).Update("attention_attempts", start)
		const msg = "ERROR: You've hit your usage limit. try again at 8:00 PM."
		for round := 0; round < 3; round++ {
			f.fail(t, msg)
			c := f.claim(t, 9)
			reset := time.Now().Add(3 * time.Hour)
			if out := f.resolveResult(t, c, &client.AttentionResult{Wait: "usage_limit", Reason: msg, ResetAt: &reset}); out.Code != 204 {
				t.Fatalf("start %d round %d: wait %d", start, round, out.Code)
			}
			if got := f.fresh(t).AttentionAttempts; got != start {
				t.Fatalf("start %d round %d: a limit changed attempts to %d", start, round, got)
			}
			f.h.DB.Model(&db.Ticket{}).Where("id = ?", f.ticket.ID).Update("attention_wait_until", time.Now().Add(-time.Minute))
			c = f.claim(t, 9)
			if out := f.resolveResult(t, c, nil); out.Code != 200 {
				t.Fatalf("resume: %d", out.Code)
			}
			if got := f.fresh(t).AttentionAttempts; got != start {
				t.Fatalf("start %d round %d: resume changed attempts to %d", start, round, got)
			}
		}
		f.fail(t, "assertion failed: expected 3 got 4")
		c := f.claim(t, 9)
		if out := f.resolveResult(t, c, &client.AttentionResult{Retry: true, Reason: "fix", Feedback: "fix the test"}); out.Code != 200 {
			t.Fatalf("retry: %d", out.Code)
		}
		if got := f.fresh(t).AttentionAttempts; got != start+1 {
			t.Fatalf("start %d: a real failure should cost exactly one attempt, got %d", start, got)
		}
	}
}

const limitChecks = 24 // the api package's maxLimitChecks

func TestParsedLimitThatNeverLiftsEscalates(t *testing.T) {
	f := newRecoveryHarness(t)
	const msg = "You've hit your usage limit. try again in 6 hours."
	f.fail(t, msg)
	waits := 0
	for range limitChecks + 2 {
		reset := time.Now().Add(6 * time.Hour)
		if f.limitWait(t, msg, &reset) != 204 {
			break
		}
		waits++
		f.advance(t, time.Until(*f.fresh(t).AttentionWaitUntil)+time.Second)
		c := f.claim(t, 3)
		f.resolveResult(t, c, nil)
		f.fail(t, msg)
	}
	if waits != limitChecks {
		t.Fatalf("waited %d times, want exactly %d before asking a human", waits, limitChecks)
	}
}
