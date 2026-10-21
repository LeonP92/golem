package api

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/leonp92/golem/internal/orchestrator/auth"
	"github.com/leonp92/golem/internal/orchestrator/db"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// A failure source must explicitly mark recoverable work. Merely having the
// badge never authorizes a retry (in particular, human parks and legacy rows).
func markAttention(updates map[string]any, t db.Ticket, reason string) {
	fingerprint := attentionFingerprint(reason)
	repeats := 1
	if fingerprint == t.AttentionFingerprint {
		repeats = t.AttentionRepeats + 1
	}
	updates["attention_reason"] = reason
	updates["attention_fingerprint"] = fingerprint
	updates["attention_repeats"] = repeats
	updates["attention_eligible"] = repeats < 2
	if repeats >= 2 {
		updates["attention_reason"] = "Same failure twice; human attention required.\n\n" + reason
	}
}

var volatileFailureText = regexp.MustCompile(`\b[0-9a-fA-F]{7,64}\b|\b\d{4}-\d\d-\d\d[T ][0-9:.Z+-]+\b`)

func attentionFingerprint(reason string) string {
	normalized := strings.Join(strings.Fields(strings.ToLower(volatileFailureText.ReplaceAllString(reason, "<value>"))), " ")
	sum := sha256.Sum256([]byte(normalized))
	return hex.EncodeToString(sum[:])
}

// Reuse the existing ownership, intake and epic guards on both dispatch and
// completion. A diagnosis is evidence, never authority to bypass these gates.
const attentionGuard = "assigned_shem = ? AND phase = 'needs-attention' AND attention_eligible AND " +
	"(issue_number IS NULL OR (intake_approved AND approved_body_hash <> '' AND approved_body_hash = body_hash)) AND " + epicGateResume

func (h *Handlers) attentionTickets(w http.ResponseWriter, r *http.Request) {
	var tickets []db.Ticket
	err := h.DB.Where(attentionGuard, auth.ShemFromRequest(r).ID).
		Where("attention_lease_until IS NULL OR attention_lease_until < ?", time.Now()).Find(&tickets).Error
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	type pendingAttention struct {
		TicketID   string `json:"ticket_id"`
		RepoRemote string `json:"repo_remote"`
	}
	out := make([]pendingAttention, 0, len(tickets))
	for _, t := range tickets {
		out = append(out, pendingAttention{TicketID: t.ID, RepoRemote: t.RepoRemote})
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(out) //nolint:errcheck
}

type attentionClaim struct {
	Claim   *ClaimResponse `json:"claim"`
	Token   string         `json:"token"`
	Reason  string         `json:"reason"`
	Attempt int            `json:"attempt"`
}

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
	until := time.Now().Add(10 * time.Minute)
	exhausted := false
	err := h.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where(attentionGuard, shem.ID).
			Where("id = ? AND (attention_lease_until IS NULL OR attention_lease_until < ?)", id, time.Now()).First(&t).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return errNotRetryable
			}
			return err
		}
		if t.AttentionAttempts >= body.MaxAttempts {
			exhausted = true
			reason := fmt.Sprintf("Stopping after %d automatic recovery attempt(s). Human attention required.\n\n%s", t.AttentionAttempts, t.AttentionReason)
			if err := tx.Model(&t).Updates(map[string]any{"attention_eligible": false, "attention_reason": reason}).Error; err != nil {
				return err
			}
			return appendLogTx(tx, id, "BLOCKER", "orchestrator", "", reason)
		}
		phase := retryPhaseForTicket(t)
		if t.CheckpointSHA == nil || strings.TrimSpace(*t.CheckpointSHA) == "" {
			exhausted = true
			reason := "Automatic recovery needs a saved phase and commit checkpoint; use manual requeue."
			if err := tx.Model(&t).Updates(map[string]any{"attention_eligible": false, "attention_reason": reason}).Error; err != nil {
				return err
			}
			return appendLogTx(tx, id, "BLOCKER", "orchestrator", "", reason)
		}
		t.RetryPhase = phase
		return tx.Model(&t).Updates(map[string]any{"attention_token": token, "attention_lease_until": until,
			"attention_attempts": gorm.Expr("attention_attempts + 1"), "retry_phase": phase}).Error
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
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(attentionClaim{h.claimResponse(t, row), token, t.AttentionReason, t.AttentionAttempts + 1}) //nolint:errcheck
}

func (h *Handlers) resolveAttention(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Token    string `json:"token"`
		Retry    bool   `json:"retry"`
		Feedback string `json:"feedback"`
		Reason   string `json:"reason"`
	}
	if json.NewDecoder(r.Body).Decode(&body) != nil || body.Token == "" || strings.TrimSpace(body.Reason) == "" || (body.Retry && strings.TrimSpace(body.Feedback) == "") {
		http.Error(w, "token, reason and retry feedback are required", http.StatusBadRequest)
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
		updates := map[string]any{"attention_eligible": false, "attention_token": "", "attention_lease_until": nil, "attention_reason": body.Reason}
		if body.Retry {
			phase := retryWorkerPhase(t.RetryPhase)
			if phase == "" {
				return errNotRetryable
			}
			// Stay on this shem to preserve dirty work. The existing retry resume
			// path reconstructs this branch without resetting a matching worktree.
			updates["phase"] = phase
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
	if !body.Retry {
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
