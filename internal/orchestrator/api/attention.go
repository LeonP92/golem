package api

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/leonp92/golem/internal/orchestrator/auth"
	"github.com/leonp92/golem/internal/orchestrator/db"
	"github.com/leonp92/golem/internal/orchestrator/ws"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Wait kinds a shem may propose. The server owns the length and budget of every
// wait; the shem only classifies the failure and, for a usage limit, reports
// the reset time it parsed from the backend's own message.
const (
	waitTransient  = "transient"
	waitUsageLimit = "usage_limit"
)

// Transient waits: five checks with backoff (1+2+4+8+15 minutes) fill exactly
// one 30-minute incident deadline.
const (
	maxWaitChecks      = 5
	maxLimitChecks     = 24 // eight days at the 8h usage_limit_max
	waitIncidentWindow = 30 * time.Minute
)

// WaitPolicy holds the configurable caps for usage-limit waits. Zero fields
// take the defaults (see config.SupervisorConfig).
type WaitPolicy struct {
	// UsageLimitMax caps a single wait for a parsed reset time.
	UsageLimitMax time.Duration
	// LimitBackoff is the schedule used when no reset time could be parsed;
	// its last step repeats until LimitBackoffMax is spent.
	LimitBackoff    []time.Duration
	LimitBackoffMax time.Duration
}

func (p WaitPolicy) withDefaults() WaitPolicy {
	if p.UsageLimitMax <= 0 {
		p.UsageLimitMax = 8 * time.Hour
	}
	if len(p.LimitBackoff) == 0 {
		p.LimitBackoff = []time.Duration{15 * time.Minute, 30 * time.Minute, time.Hour, 2 * time.Hour}
	}
	if p.LimitBackoffMax <= 0 {
		p.LimitBackoffMax = 6 * time.Hour
	}
	return p
}

func waitBackoff(check int) time.Duration {
	switch check {
	case 1:
		return time.Minute
	case 2:
		return 2 * time.Minute
	case 3:
		return 4 * time.Minute
	case 4:
		return 8 * time.Minute
	default:
		return 15 * time.Minute
	}
}

// A failure source must explicitly mark recoverable work. Merely having the
// badge never authorizes a retry (in particular, human parks and legacy rows).
//
// A repeat of the failure an open supervisor wait incident is bound to is the
// wait working as designed, not "the same failure twice": the transient fault
// is expected to persist while the incident's checks and deadline run. The
// incident budget, not the repeat detector, is what eventually escalates it.
func markAttention(updates map[string]any, t db.Ticket, reason string) {
	fingerprint := attentionFingerprint(reason)
	repeats := 1
	if fingerprint == t.AttentionFingerprint {
		repeats = t.AttentionRepeats + 1
	}
	updates["attention_reason"] = reason
	updates["attention_fingerprint"] = fingerprint
	updates["attention_repeats"] = repeats
	updates["attention_eligible"] = true
	if repeats >= 2 && !t.WaitIncidentOpen() {
		updates["attention_eligible"] = false
		updates["attention_reason"] = "Same failure twice; human attention required.\n\n" + reason
	}
}

var volatileFailureText = regexp.MustCompile(`(?i)\b[0-9a-f]{7,64}\b|\b\d{4}-\d\d-\d\d[T ][0-9:.Z+-]+\b|\b\d{1,2}(:\d\d)?\s?[ap]m\b|\b\d+\s*(?:days?|d|hours?|hrs?|h|minutes?|mins?|m|seconds?|secs?|s)\b`)

func attentionFingerprint(reason string) string {
	normalized := strings.Join(strings.Fields(strings.ToLower(volatileFailureText.ReplaceAllString(reason, "<value>"))), " ")
	sum := sha256.Sum256([]byte(normalized))
	return hex.EncodeToString(sum[:])
}

// Reuse the existing ownership, intake and epic guards on both dispatch and
// completion. A diagnosis is evidence, never authority to bypass these gates.
//
// NOT manual_approval is the supervisor mode gate: a ticket in manual
// movement keeps its human checkpoints and is never auto-retried, auto-decided,
// waited or recovered. The human releases it with retry-current-step instead.
const attentionGuard = "assigned_shem = ? AND phase = 'needs-attention' AND attention_eligible AND NOT manual_approval AND " +
	"(issue_number IS NULL OR (intake_approved AND approved_body_hash <> '' AND approved_body_hash = body_hash)) AND " + epicGateResume

// waitDue keeps a waiting ticket out of every automatic entry point until its
// next check is due.
const waitDue = "(attention_wait_until IS NULL OR attention_wait_until < ?)"

// supervisedTickets is the read behind the ticket list's shem filters:
// the caller's needs-attention tickets due for a recovery claim, or with
// migratable the subset waiting out this shem's usage limit that it may move
// to the pool (see migrateTicket). The migratable listing and the migrate
// call share migratableWhere, so a listed ticket cannot pass one check and
// fail another.
func (h *Handlers) supervisedTickets(shemID uint, migratable bool) ([]db.Ticket, error) {
	var tickets []db.Ticket
	var err error
	if migratable {
		err = h.DB.Where(migratableWhere, shemID, time.Now()).Find(&tickets).Error
	} else {
		err = h.DB.Where(attentionGuard, shemID).
			Where(waitDue, time.Now()).Find(&tickets).Error
	}
	return tickets, err
}

// attentionTickets is the deprecated pre-filter alias of
// GET /api/tickets?attention=true, kept for shems built before the read
// moved onto the list endpoint. It answers the plain attention mode only;
// the migratable read never shipped on this route.
func (h *Handlers) attentionTickets(w http.ResponseWriter, r *http.Request) {
	tickets, err := h.supervisedTickets(auth.ShemFromRequest(r).ID, false)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	type pendingAttention struct {
		TicketID   string `json:"ticket_id"`
		RepoRemote string `json:"repo_remote"`
		Reason     string `json:"reason,omitempty"`
	}
	out := make([]pendingAttention, 0, len(tickets))
	for _, t := range tickets {
		out = append(out, pendingAttention{TicketID: t.ID, RepoRemote: t.RepoRemote, Reason: t.AttentionReason})
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(out) //nolint:errcheck
}

type attentionClaim struct {
	Claim   *ClaimResponse `json:"claim"`
	Token   string         `json:"token"`
	Reason  string         `json:"reason"`
	Attempt int            `json:"attempt"`
	// WaitResume marks a claim whose wait just elapsed: the shem resumes the
	// failed step directly instead of diagnosing a stale failure.
	WaitResume bool `json:"wait_resume,omitempty"`
}

// attentionLease is how long a recovery claim holds a ticket.
const attentionLease = 10 * time.Minute

func (h *Handlers) claimAttention(w http.ResponseWriter, r *http.Request) {
	var body struct {
		MaxAttempts int `json:"max_attempts"`
	}
	if json.NewDecoder(r.Body).Decode(&body) != nil || body.MaxAttempts < 1 {
		http.Error(w, "max_attempts must be positive", http.StatusBadRequest)
		return
	}
	id := r.PathValue("id")
	shem := auth.ShemFromRequest(r)
	var t db.Ticket
	token := uuid.NewString()
	until := time.Now().Add(attentionLease)
	exhausted := false
	err := h.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where(attentionGuard, shem.ID).
			Where("id = ? AND (attention_lease_until IS NULL OR attention_lease_until < ?) AND "+waitDue, id, time.Now(), time.Now()).First(&t).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return errNotRetryable
			}
			return err
		}
		// A wait-incident claim continues the incident; only a fresh failure
		// spends the diagnosis/repair budget.
		waitIncident := t.WaitIncidentOpen()
		if !waitIncident && t.AttentionAttempts >= body.MaxAttempts {
			exhausted = true
			reason := fmt.Sprintf("Stopping after %d automatic recovery attempt(s). Human attention required.\n\n%s", t.AttentionAttempts, t.AttentionReason)
			if err := tx.Model(&t).Updates(map[string]any{"attention_eligible": false, "attention_reason": reason}).Error; err != nil {
				return err
			}
			return appendLogTx(tx, id, "BLOCKER", "orchestrator", "", reason)
		}
		if t.CheckpointSHA == nil || strings.TrimSpace(*t.CheckpointSHA) == "" {
			exhausted = true
			reason := "Automatic recovery needs a saved phase and commit checkpoint; use manual requeue."
			if err := tx.Model(&t).Updates(map[string]any{"attention_eligible": false, "attention_reason": reason}).Error; err != nil {
				return err
			}
			return appendLogTx(tx, id, "BLOCKER", "orchestrator", "", reason)
		}
		phase := retryPhaseForTicket(t)
		t.RetryPhase = phase
		updates := map[string]any{"attention_token": token, "attention_lease_until": until, "retry_phase": phase}
		if !waitIncident {
			updates["attention_attempts"] = gorm.Expr("attention_attempts + 1")
		}
		return tx.Model(&t).Updates(updates).Error
	})
	if errors.Is(err, errNotRetryable) || (err == nil && exhausted) {
		http.Error(w, "recovery not available", http.StatusConflict)
		return
	}
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	row, err := h.shemRow(shem.ID)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	attempt := t.AttentionAttempts
	if !t.WaitIncidentOpen() {
		attempt++
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(attentionClaim{h.claimResponse(t, row), token, t.AttentionReason, attempt, t.AttentionWaitUntil != nil}) //nolint:errcheck
}

// attentionDecision is the decided route's payload, recorded with the resume
// in one transaction. Fields mirror the diagnoser's contract.
type attentionDecision struct {
	Question string `json:"question"`
	Choice   string `json:"choice"`
	Reason   string `json:"reason"`
	Basis    string `json:"basis"`
}

func (d *attentionDecision) complete() bool {
	return d != nil && strings.TrimSpace(d.Question) != "" && strings.TrimSpace(d.Choice) != "" &&
		strings.TrimSpace(d.Reason) != "" && strings.TrimSpace(d.Basis) != ""
}

func (h *Handlers) resolveAttention(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Token    string             `json:"token"`
		Retry    bool               `json:"retry"`
		Feedback string             `json:"feedback"`
		Reason   string             `json:"reason"`
		Wait     string             `json:"wait,omitempty"`
		ResetAt  *time.Time         `json:"reset_at,omitempty"`
		Decision *attentionDecision `json:"decision,omitempty"`
	}
	if json.NewDecoder(r.Body).Decode(&body) != nil || body.Token == "" || strings.TrimSpace(body.Reason) == "" || (body.Retry && strings.TrimSpace(body.Feedback) == "") {
		http.Error(w, "token, reason and retry feedback are required", http.StatusBadRequest)
		return
	}
	// Exactly one outcome per resolve.
	outcomes := 0
	for _, b := range []bool{body.Retry, body.Wait != ""} {
		if b {
			outcomes++
		}
	}
	if outcomes > 1 || (body.Decision != nil && !body.Retry) || (body.Wait != "" && body.Wait != waitTransient && body.Wait != waitUsageLimit) {
		http.Error(w, "one recovery outcome per result; a decision rides a retry", http.StatusBadRequest)
		return
	}
	if body.Decision != nil && !body.Decision.complete() {
		http.Error(w, "a decision needs question, choice, reason and basis", http.StatusBadRequest)
		return
	}
	id := r.PathValue("id")
	shem := auth.ShemFromRequest(r)
	var fresh db.Ticket
	err := h.DB.Transaction(func(tx *gorm.DB) error {
		var t db.Ticket
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where(attentionGuard, shem.ID).
			Where("id = ? AND attention_token = ? AND attention_lease_until > ?", id, body.Token, time.Now()).First(&t).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return errNotRetryable
			}
			return err
		}
		if body.Wait != "" {
			return h.resolveWait(tx, &t, body.Reason, body.Wait, body.ResetAt)
		}
		updates := map[string]any{"attention_eligible": false, "attention_token": "", "attention_lease_until": nil, "attention_reason": body.Reason}
		if body.Retry {
			phase := retryWorkerPhase(t.RetryPhase)
			if phase == "" {
				return errNotRetryable
			}
			// Stay on this shem to preserve dirty work. The existing retry resume
			// path reconstructs this branch without resetting a matching worktree.
			updates["phase"] = phase
			// The wait check is consumed; the incident stays bound to its
			// fingerprint so a repeat of the same transient fault continues
			// the wait instead of tripping the repeat detector.
			updates["attention_wait_until"] = nil
			if err := tx.Create(&db.HumanInput{TicketID: id, Kind: "feedback", Prompt: body.Feedback, CreatedAt: time.Now()}).Error; err != nil {
				return err
			}
		}
		if err := tx.Model(&t).Updates(updates).Error; err != nil {
			return err
		}
		if err := appendLogTx(tx, id, "STATUS", "diagnoser", "developer", body.Reason+"\n\n"+body.Feedback); err != nil {
			return err
		}
		// The decided route's record commits with the resume it authorizes —
		// never a decision whose retry rolled back, never a retry whose
		// decision was lost to a failed best-effort post.
		if body.Decision != nil {
			d := body.Decision
			msg := fmt.Sprintf("%s -> %s: %s (basis: %s)", d.Question, d.Choice, d.Reason, d.Basis)
			if err := appendLogTx(tx, id, "DECISION", "diagnoser", "", msg); err != nil {
				return err
			}
		}
		if err := tx.First(&fresh, "id = ?", id).Error; err != nil {
			return err
		}
		return enqueueGitHubPhase(tx, fresh, fresh.Phase, h.BaseURL)
	})
	if errors.Is(err, errNotRetryable) {
		http.Error(w, "recovery changed or expired", http.StatusConflict)
		return
	}
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if body.Wait != "" || !body.Retry {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	row, err := h.shemRow(shem.ID)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(h.claimResponse(fresh, row)) //nolint:errcheck
}

// resolveWait parks the ticket on the supervisor's clock rather than a
// human's. The shem only names the kind (and a parsed reset time); the server
// owns the delay, the jitter, the per-incident budget and the deadline, and
// refuses (conflict) once the budget is spent, so the shem escalates to a human.
//
// A usage limit waits until its reset (plus 60-120 s jitter, capped by
// UsageLimitMax). With no usable reset time it backs off on the configured
// schedule inside LimitBackoffMax. A transient fault keeps the short schedule.
func (h *Handlers) resolveWait(tx *gorm.DB, t *db.Ticket, reason, kind string, resetAt *time.Time) error {
	fingerprint := attentionFingerprint(reason)
	if fingerprint != t.AttentionFingerprint {
		// The claim's failure is not the ticket's current one.
		return errNotRetryable
	}
	p := h.Wait.withDefaults()
	now := time.Now()
	checks, deadline := t.AttentionWaitChecks, t.AttentionWaitDeadline
	if t.AttentionWaitFingerprint != fingerprint || deadline == nil || !deadline.After(now) {
		window := waitIncidentWindow
		if kind == waitUsageLimit {
			window = p.LimitBackoffMax
		}
		checks, deadline = 0, ptrTime(now.Add(window))
	}
	checks++
	if kind == waitUsageLimit && checks > maxLimitChecks {
		return errNotRetryable // a limit that never lifts goes to a human
	}
	var until time.Time
	switch {
	case kind == waitUsageLimit && resetAt != nil && resetAt.After(now):
		until = resetAt.Add(time.Minute + time.Duration(rand.Int63n(int64(time.Minute))))
		if limit := now.Add(p.UsageLimitMax); until.After(limit) {
			until = limit
		}
		// The incident outlives the reset so the resumed step's repeat of the
		// same limit continues it instead of reading as "same failure twice".
		if grace := until.Add(waitIncidentWindow); grace.After(*deadline) {
			deadline = &grace
		}
	case kind == waitUsageLimit:
		until = now.Add(p.LimitBackoff[min(checks, len(p.LimitBackoff))-1])
		if until.After(*deadline) {
			until = *deadline
		}
	default:
		if checks > maxWaitChecks || !deadline.After(now) {
			return errNotRetryable
		}
		until = now.Add(waitBackoff(checks))
		if until.After(*deadline) {
			until = *deadline
		}
	}
	updates := map[string]any{
		"attention_wait_until":       until,
		"attention_wait_checks":      checks,
		"attention_wait_deadline":    deadline,
		"attention_wait_fingerprint": fingerprint,
		"attention_wait_kind":        kind,
		"attention_token":            "",
		"attention_lease_until":      nil,
	}
	// A fault the supervisor waits out is not a failed attempt. The claim that
	// brought us here spent one unless it continued an open incident, so give
	// exactly that one back; a genuine code failure never reaches this point
	// and keeps what it spent. The incident is judged as the claim saw it
	// (claim time = lease end minus the lease), not by the clock now.
	if t.AttentionLeaseUntil != nil && t.AttentionAttempts > 0 {
		claimedAt := t.AttentionLeaseUntil.Add(-attentionLease)
		continued := t.AttentionWaitFingerprint != "" && t.AttentionWaitDeadline != nil &&
			t.AttentionWaitDeadline.After(claimedAt) && t.AttentionWaitFingerprint == t.AttentionFingerprint
		if !continued {
			updates["attention_attempts"] = t.AttentionAttempts - 1
		}
	}
	if err := tx.Model(t).Updates(updates).Error; err != nil {
		return err
	}
	if kind == waitUsageLimit && t.AssignedShem != nil {
		// Kept on the shem so a human retry or requeue, which deletes the
		// wait above, or a restart cannot lift the hold early.
		if err := tx.Model(&db.Shem{}).Where("id = ? AND (limited_until IS NULL OR limited_until < ?)", *t.AssignedShem, until).
			Update("limited_until", until).Error; err != nil {
			return err
		}
	}
	msg := fmt.Sprintf("Supervisor waits out a %s fault (check %d, resumes %s, incident deadline %s). No attempt is spent.\n\n%s",
		strings.ReplaceAll(kind, "_", " "), checks, until.UTC().Format(time.RFC3339), deadline.UTC().Format(time.RFC3339), reason)
	return appendLogTx(tx, t.ID, "STATUS", "supervisor", "", msg)
}

// resetAttention is what a human retry or requeue clears: the automatic
// attempt counter, the repeat detector and any open wait. A ticket a person
// released starts its next failure with a full budget.
func resetAttention() map[string]any {
	return map[string]any{
		"attention_attempts": 0, "attention_repeats": 0, "attention_fingerprint": "",
		"attention_wait_until": nil, "attention_wait_checks": 0, "attention_wait_deadline": nil,
		"attention_wait_fingerprint": "", "attention_wait_kind": "",
		"migrated_backend": "", "migrations": 0,
	}
}

// reportBackendLimit records a usage limit the calling shem observed, whether
// or not a ticket wait follows (the failing phase may not be supervisable, for
// example a manual_approval ticket). It only moves the caller's own row, only
// later, and never past the configured maximum.
func (h *Handlers) reportBackendLimit(w http.ResponseWriter, r *http.Request) {
	var body struct {
		LimitedUntil time.Time `json:"limited_until"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}
	now := time.Now()
	until := body.LimitedUntil
	if max := now.Add(h.Wait.withDefaults().UsageLimitMax); until.After(max) {
		until = max
	}
	if !until.After(now) {
		http.Error(w, "limited_until must be in the future", http.StatusBadRequest)
		return
	}
	err := h.DB.Model(&db.Shem{}).Where("id = ? AND (limited_until IS NULL OR limited_until < ?)", auth.ShemFromRequest(r).ID, until).
		Update("limited_until", until).Error
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// migratableWhere selects tickets this shem may move off itself: waiting on a
// usage limit, automatic (no manual_approval), not pinned to a backend, not
// moved before, with a checkpoint to resume from. The ?migratable=true listing
// and the migrate call share it, so a listed ticket cannot go stale on one
// check and pass another.
const migratableWhere = "assigned_shem = ? AND phase = 'needs-attention' AND attention_wait_kind = 'usage_limit' AND attention_wait_until > ? AND " +
	"NOT manual_approval AND model_backend = '' AND migrations = 0 AND checkpoint_sha IS NOT NULL AND checkpoint_sha <> ''"

// migrateTicket moves a limit-waiting ticket off the calling shem so a shem
// that is not limited can try it. The caller says which checkpoint it checked
// was clean and on the remote; a different one is refused. The ticket keeps
// its retry phase and checkpoint, its attempt count, and its wait and token are
// cleared, so the old owner's results are rejected from here on. Only shems
// of the same backend may claim it (MigratedBackend), and it moves once.
func (h *Handlers) migrateTicket(w http.ResponseWriter, r *http.Request) {
	var body struct {
		CheckpointSHA string `json:"checkpoint_sha"`
		// CapturedSHA is the commit the limited shem made of its unfinished
		// work and pushed (a descendant of CheckpointSHA, which it checked).
		// It becomes the checkpoint the next shem resumes from.
		CapturedSHA string `json:"captured_sha"`
	}
	if json.NewDecoder(r.Body).Decode(&body) != nil || body.CheckpointSHA == "" {
		http.Error(w, "checkpoint_sha required", http.StatusBadRequest)
		return
	}
	id := r.PathValue("id")
	shem, err := h.shemRow(auth.ShemFromRequest(r).ID)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	var repo string
	err = h.DB.Transaction(func(tx *gorm.DB) error {
		var t db.Ticket
		if err := tx.Where(migratableWhere+" AND id = ? AND checkpoint_sha = ?", shem.ID, time.Now(), id, body.CheckpointSHA).First(&t).Error; err != nil {
			return errNotRetryable
		}
		phase := retryPhaseForTicket(t)
		if phase == "" {
			return errNotRetryable
		}
		updates := map[string]any{"phase": "unassigned", "assigned_shem": nil, "retry_phase": phase,
			"attention_token": "", "attention_lease_until": nil,
			"attention_wait_until": nil, "attention_wait_checks": 0, "attention_wait_deadline": nil,
			"attention_wait_fingerprint": "", "attention_wait_kind": "",
			"migrated_backend": shem.Backend, "migrations": 1,
			// A failure on the new shem is a first failure there, not "the same failure twice".
			"attention_fingerprint": "", "attention_repeats": 0}
		if body.CapturedSHA != "" {
			updates["checkpoint_sha"] = body.CapturedSHA
		}
		res := tx.Model(&db.Ticket{}).Where("id = ? AND assigned_shem = ? AND phase = 'needs-attention' AND migrations = 0", id, shem.ID).Updates(updates)
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return errNotRetryable
		}
		repo = t.RepoRemote
		var fresh db.Ticket
		if err := tx.First(&fresh, "id = ?", id).Error; err != nil {
			return err
		}
		msg := "Usage limit on shem " + shem.Name + ": ticket moved to the pool at phase " + phase +
			" from checkpoint " + firstNonEmpty(body.CapturedSHA, body.CheckpointSHA) + ". A shem of backend " + shem.Backend + " may try it; no attempt was spent."
		if err := appendLogTx(tx, id, "STATUS", "supervisor", "", msg); err != nil {
			return err
		}
		return enqueueGitHubPhase(tx, fresh, "unassigned", h.BaseURL)
	})
	if errors.Is(err, errNotRetryable) {
		http.Error(w, "ticket is not migratable", http.StatusConflict)
		return
	}
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	h.Hub.Broadcast(repo, ws.WSMessage{Type: "ticket_available", TicketID: strPtr(id), Repo: repo})
	w.WriteHeader(http.StatusNoContent)
}

// releaseTicket gives back a ticket this shem claimed but has not started: the
// backend became limited between claim and run. Only "claimed" qualifies; a
// ticket with any work or checkpoint stays where its worktree is.
func (h *Handlers) releaseTicket(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	shem := auth.ShemFromRequest(r)
	err := h.DB.Transaction(func(tx *gorm.DB) error {
		res := tx.Model(&db.Ticket{}).Where("id = ? AND phase = 'claimed' AND assigned_shem = ? AND checkpoint_sha IS NULL", id, shem.ID).
			Updates(map[string]any{"phase": "unassigned", "assigned_shem": nil})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return errNotRetryable
		}
		var fresh db.Ticket
		if err := tx.First(&fresh, "id = ?", id).Error; err != nil {
			return err
		}
		return enqueueGitHubPhase(tx, fresh, "unassigned", h.BaseURL)
	})
	if errors.Is(err, errNotRetryable) {
		http.Error(w, "ticket is not releasable", http.StatusConflict)
		return
	}
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func ptrTime(t time.Time) *time.Time { return &t }

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
