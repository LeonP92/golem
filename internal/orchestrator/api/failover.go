package api

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/leonp92/golem/internal/models"
	"github.com/leonp92/golem/internal/orchestrator/db"
	"gorm.io/gorm"
)

// failover hands a ticket whose backend is limited for a long time to a shem of
// the backend the operator named for it (supervisor.failover), instead of
// waiting the limit out. The ticket keeps its branch state: its model choices
// become tier references, so the new backend picks its own model of the same
// tier (a plan on a large model stays on a large model), and it resumes from
// the same checkpoint on the shared checkout.
//
// It moves a ticket only when all of these hold: a target backend is configured
// for the ticket's current one, the wait would last at least FailoverAfter, a
// shem of that backend that serves the repo is online and not itself limited,
// and the repo is local://. shortcut: local:// only, because there the shems
// share one checkout; a remote repo needs the capture-and-push that
// migrateWaiting does, which stays same-backend; extend it when a remote repo
// needs cross-backend moves.
//
// The caller has already validated the claim's token. It reports whether the
// ticket moved; false leaves the ticket for the normal wait.
func (h *Handlers) failover(tx *gorm.DB, t *db.Ticket, reason string, resetAt *time.Time) (bool, error) {
	p := h.Wait.withDefaults()
	if t.AssignedShem == nil || len(p.Failover) == 0 || !strings.HasPrefix(t.RepoRemote, "local://") {
		return false, nil
	}
	if attentionFingerprint(reason) != t.AttentionFingerprint {
		return false, nil // not this ticket's current failure; the wait path reports that
	}
	now := time.Now()
	if long := (resetAt != nil && resetAt.Sub(now) >= p.FailoverAfter) || t.AttentionWaitChecks >= 1; !long {
		return false, nil
	}
	var src db.Shem
	if err := tx.First(&src, "id = ?", *t.AssignedShem).Error; err != nil {
		return false, err
	}
	target := p.Failover[src.Backend]
	if target == "" || target == src.Backend {
		return false, nil
	}
	var candidates []db.Shem
	if err := tx.Where("backend = ? AND status = 'online' AND (limited_until IS NULL OR limited_until < ?)", target, now).Find(&candidates).Error; err != nil {
		return false, err
	}
	var dst *db.Shem
	for i := range candidates {
		var repos []string
		if json.Unmarshal([]byte(candidates[i].Repos), &repos) == nil && slices.Contains(repos, t.RepoRemote) {
			dst = &candidates[i]
			break
		}
	}
	if dst == nil {
		return false, nil
	}
	phase := ""
	if t.CheckpointSHA != nil && strings.TrimSpace(*t.CheckpointSHA) != "" {
		phase = retryPhaseForTicket(*t)
	}
	selJSON, _ := json.Marshal(tierSelections(src.ModelCatalog(), t.ModelSelections()))
	updates := map[string]any{"phase": "unassigned", "assigned_shem": nil, "retry_phase": phase,
		"model_backend": target, "models": string(selJSON), "attention_token": "", "attention_lease_until": nil,
		"attention_eligible": false, "migrations": 0, "migrated_backend": ""}
	for k, v := range resetAttention() {
		updates[k] = v
	}
	if err := tx.Model(&db.Ticket{}).Where("id = ? AND assigned_shem = ? AND phase = 'needs-attention'", t.ID, *t.AssignedShem).Updates(updates).Error; err != nil {
		return false, err
	}
	msg := fmt.Sprintf("Usage limit on %s shem %s: the ticket moves to a %s shem instead of waiting. It keeps its checkpoint; model choices carry over by tier. No attempt is spent.",
		src.Backend, src.Name, target)
	if err := appendLogTx(tx, t.ID, "STATUS", "supervisor", "", msg); err != nil {
		return false, err
	}
	var fresh db.Ticket
	if err := tx.First(&fresh, "id = ?", t.ID).Error; err != nil {
		return false, err
	}
	return true, enqueueGitHubPhase(tx, fresh, "unassigned", h.BaseURL)
}

// tierSelections rewrites model ids as tier references using the catalog they
// came from, so another backend resolves the same tier to its own model. A
// value that is already a tier reference, or an id the catalog does not know,
// is kept as it is (the new backend then falls back to its stage default).
func tierSelections(src models.Catalog, sel models.Selections) models.Selections {
	out := models.Selections{}
	for stage, v := range sel {
		out[stage] = v
		if _, isTier := models.TierRef(v); isTier {
			continue
		}
		for _, m := range src.Models {
			if m.ID == v && m.Tier != "" {
				out[stage] = models.TierValue(m.Tier)
				break
			}
		}
	}
	return out
}
