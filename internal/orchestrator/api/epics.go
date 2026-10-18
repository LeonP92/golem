package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/leonp92/golem/internal/epic"
	"github.com/leonp92/golem/internal/orchestrator/auth"
	"github.com/leonp92/golem/internal/orchestrator/config"
	"github.com/leonp92/golem/internal/orchestrator/db"
	"github.com/leonp92/golem/internal/orchestrator/epics"
	"github.com/leonp92/golem/internal/orchestrator/ghsync"
	"github.com/leonp92/golem/internal/orchestrator/rbac"
	"github.com/leonp92/golem/internal/orchestrator/urlnorm"
	"github.com/leonp92/golem/internal/orchestrator/ws"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// RegisterEpicRoutes adds the Epic Run routes.
func (h *Handlers) RegisterEpicRoutes(mux *http.ServeMux) {
	session := func(p rbac.Permission, f http.HandlerFunc) http.Handler {
		return auth.RequireSession(h.DB)(rbac.Require(p)(f))
	}
	write := func(p rbac.Permission, f http.HandlerFunc) http.Handler {
		return auth.RequireSession(h.DB)(auth.RequireCSRF(rbac.Require(p)(f)))
	}
	mux.Handle("POST /api/epics", write(rbac.PermTicketCreate, h.createEpic))
	mux.Handle("GET /api/epics", session(rbac.PermTicketView, h.listEpics))
	mux.Handle("GET /api/epics/{id}", session(rbac.PermTicketView, h.getEpic))
	mux.Handle("POST /api/epics/{id}/revisions", write(rbac.PermTicketCreate, h.addEpicRevision))
	mux.Handle("POST /api/epics/{id}/approve", write(rbac.PermTicketManage, h.approveEpic))
	mux.Handle("POST /api/epics/{id}/start", write(rbac.PermTicketManage, h.startEpic))
	mux.Handle("POST /api/epics/{id}/nodes/{key}/integrate", write(rbac.PermTicketManage, h.integrateEpicNode))
	mux.Handle("POST /api/epics/{id}/merge-final", write(rbac.PermTicketManage, h.mergeFinalEpic))
	mux.Handle("POST /api/epics/{id}/pause", write(rbac.PermTicketManage, h.pauseEpic))
	mux.Handle("POST /api/epics/{id}/resume", write(rbac.PermTicketManage, h.resumeEpic))
	// Shem-authenticated: the tester posts verdicts for the child it holds,
	// and the shem that did a child's work runs the integration lane through
	// the gate check and the result receipt.
	mux.Handle("POST /api/epics/{id}/verdicts", auth.RequireAPIKey(h.DB)(http.HandlerFunc(h.postEpicVerdict)))
	mux.Handle("POST /api/epics/{id}/nodes/{key}/integrate-check", auth.RequireAPIKey(h.DB)(http.HandlerFunc(h.integrateCheckEpicNode)))
	mux.Handle("POST /api/epics/{id}/nodes/{key}/integrate-result", auth.RequireAPIKey(h.DB)(http.HandlerFunc(h.integrateEpicNodeResult)))
}

// writeEpicResult maps the epics package's errors to HTTP statuses, or
// writes body (204 when nil).
func writeEpicResult(w http.ResponseWriter, status int, body any, err error) {
	switch {
	case errors.Is(err, epics.ErrNotFound), errors.Is(err, epics.ErrRevisionMissing):
		http.Error(w, err.Error(), http.StatusNotFound)
	case errors.Is(err, epics.ErrInvalid):
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
	case errors.Is(err, epics.ErrNotEditable), errors.Is(err, epics.ErrHashMismatch),
		errors.Is(err, epics.ErrNotCurrent), errors.Is(err, epics.ErrNotApproved),
		errors.Is(err, epics.ErrWrongState), errors.Is(err, epics.ErrIntegrationConflict):
		http.Error(w, err.Error(), http.StatusConflict)
	case err != nil:
		log.Printf("epics api: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
	case body == nil:
		w.WriteHeader(http.StatusNoContent)
	default:
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		json.NewEncoder(w).Encode(body) //nolint:errcheck
	}
}

// actor names the session user on approvals (same as the UI form).
func actor(r *http.Request) string {
	if u := auth.SessionUser(r); u != nil {
		return u.Username
	}
	return ""
}

func (h *Handlers) pauseEpic(w http.ResponseWriter, r *http.Request) {
	writeEpicResult(w, 0, nil, epics.Pause(h.DB, r.PathValue("id")))
}

func (h *Handlers) resumeEpic(w http.ResponseWriter, r *http.Request) {
	if err := epics.Resume(h.DB, r.PathValue("id")); err != nil {
		writeEpicResult(w, 0, nil, err)
		return
	}
	// Children whose integration-lane wait spanned the pause are owed
	// another pass; their shems re-run the lane on this push.
	var e db.Epic
	if err := h.DB.Select("repo_remote").First(&e, "id = ?", r.PathValue("id")).Error; err == nil {
		h.Hub.Broadcast(e.RepoRemote, ws.WSMessage{Type: "epic_resumed", Repo: e.RepoRemote})
	}
	writeEpicResult(w, 0, nil, nil)
}

// mergeFinalEpic records the human final merge: the caller attests the
// integration branch is on base and names the resulting head SHA. Golem
// never merges to base itself.
func (h *Handlers) mergeFinalEpic(w http.ResponseWriter, r *http.Request) {
	var body struct {
		SHA string `json:"sha"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	writeEpicResult(w, 0, nil, epics.CompleteFinal(h.DB, r.PathValue("id"), body.SHA))
}

type createEpicRequest struct {
	RepoRemote        string `json:"repo_remote"`
	Title             string `json:"title"`
	SpecMarkdown      string `json:"spec_markdown"`
	BaseBranch        string `json:"base_branch"`
	BranchStrategy    string `json:"branch_strategy"`
	DeliveryTarget    string `json:"delivery_target"`
	AutoMergeChildren bool   `json:"auto_merge_children"`
	AutoApprovePhases bool   `json:"auto_approve_phases"`
}

func (h *Handlers) createEpic(w http.ResponseWriter, r *http.Request) {
	var body createEpicRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	p := epics.CreateParams{
		RepoRemote:        urlnorm.Normalize(body.RepoRemote),
		Title:             body.Title,
		SpecMarkdown:      body.SpecMarkdown,
		BaseBranch:        body.BaseBranch,
		BranchStrategy:    body.BranchStrategy,
		DeliveryTarget:    body.DeliveryTarget,
		AutoMergeChildren: body.AutoMergeChildren,
		AutoApprovePhases: body.AutoApprovePhases,
	}
	if u := auth.SessionUser(r); u != nil {
		p.CreatedByUserID = &u.ID
	}
	e, err := epics.Create(h.DB, p)
	writeEpicResult(w, http.StatusCreated, e, err)
}

func (h *Handlers) listEpics(w http.ResponseWriter, r *http.Request) {
	repo := r.URL.Query().Get("repo")
	if repo != "" {
		repo = urlnorm.Normalize(repo)
	}
	list, err := epics.List(h.DB, repo, r.URL.Query().Get("state"))
	writeEpicResult(w, http.StatusOK, list, err)
}

func (h *Handlers) getEpic(w http.ResponseWriter, r *http.Request) {
	var e db.Epic
	if err := h.DB.Where("id = ?", r.PathValue("id")).First(&e).Error; err != nil {
		writeEpicResult(w, 0, nil, epics.ErrNotFound)
		return
	}
	resp := map[string]any{"epic": e}
	if e.CurrentRevision > 0 {
		plan, rev, err := epics.LoadPlan(h.DB, e.ID, e.CurrentRevision)
		if err != nil {
			writeEpicResult(w, 0, nil, err)
			return
		}
		resp["plan"] = plan
		resp["revision"] = rev
	}
	writeEpicResult(w, http.StatusOK, resp, nil)
}

func (h *Handlers) addEpicRevision(w http.ResponseWriter, r *http.Request) {
	var plan epic.Plan
	if err := json.NewDecoder(r.Body).Decode(&plan); err != nil {
		http.Error(w, "invalid plan JSON", http.StatusBadRequest)
		return
	}
	rev, err := epics.AddRevision(h.DB, r.PathValue("id"), plan, r.URL.Query().Get("repo_sha"))
	writeEpicResult(w, http.StatusCreated, rev, err)
}

func (h *Handlers) approveEpic(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Revision int    `json:"revision"`
		PlanHash string `json:"plan_hash"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	writeEpicResult(w, 0, nil, epics.Approve(h.DB, r.PathValue("id"), body.Revision, body.PlanHash, actor(r)))
}

func (h *Handlers) startEpic(w http.ResponseWriter, r *http.Request) {
	created, err := epics.Start(h.DB, r.PathValue("id"))
	writeEpicResult(w, http.StatusOK, map[string]int{"created": created}, err)
}

// integrateEpicNode records a receipt by hand (repos without GitHub sync).
func (h *Handlers) integrateEpicNode(w http.ResponseWriter, r *http.Request) {
	var body struct {
		SHA string `json:"sha"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	writeEpicResult(w, 0, nil, epics.RecordIntegration(h.DB, r.PathValue("id"), r.PathValue("key"), body.SHA, ghsync.EnqueueFinalPR))
}

func (h *Handlers) postEpicVerdict(w http.ResponseWriter, r *http.Request) {
	var v db.TesterVerdict
	if err := json.NewDecoder(r.Body).Decode(&v); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	v = db.TesterVerdict{EpicID: r.PathValue("id"), EpicNodeKey: v.EpicNodeKey, HeadSHA: v.HeadSHA, BaseSHA: v.BaseSHA,
		Verdict: v.Verdict, Scenarios: v.Scenarios, Evidence: v.Evidence, Backend: v.Backend}
	// A shem vouches only for the child it holds; the final candidate
	// (empty key) is never a shem's to pass.
	if v.EpicNodeKey == "" {
		http.Error(w, "epic_node_key is required", http.StatusBadRequest)
		return
	}
	var child db.Ticket
	if err := h.DB.Where("epic_id = ? AND epic_node_key = ?", v.EpicID, v.EpicNodeKey).
		First(&child).Error; err != nil {
		http.Error(w, errNotOwner.Error(), http.StatusConflict)
		return
	}
	if !h.writeTicketOwnership(w, r, child.ID) {
		return
	}
	revise := false
	err := h.DB.Transaction(func(tx *gorm.DB) error {
		// Serialize tester decisions with gate and receipt writes on the run.
		if err := tx.Model(&db.Epic{}).Where("id = ?", v.EpicID).Update("updated_at", time.Now()).Error; err != nil {
			return err
		}
		var fresh db.Ticket
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&fresh, "id = ?", child.ID).Error; err != nil {
			return err
		}
		if fresh.AssignedShem == nil || *fresh.AssignedShem != auth.ShemFromRequest(r).ID {
			return epics.ErrIntegrationConflict
		}
		if err := epics.RecordVerdict(tx, v); err != nil {
			return err
		}
		// Retain late evidence for audit without moving a newer candidate or a
		// child already integrated. The saved checkpoint names the worker's head.
		if fresh.EpicIntegratedSHA != "" || (fresh.CheckpointSHA != nil && *fresh.CheckpointSHA != v.HeadSHA) {
			return nil
		}
		scoped := *h
		scoped.DB = tx
		switch v.Verdict {
		case "fail":
			var err error
			revise, err = scoped.bounceIntegrationTx(tx, fresh, v.HeadSHA, fmt.Sprintf("The tester failed head %s.\n\nScenarios: %s\nEvidence: %s\n\nFix the cause on the existing branch.", v.HeadSHA, v.Scenarios, v.Evidence))
			return err
		case "inconclusive":
			return scoped.parkEpicChild(fresh, fmt.Sprintf("The tester could not reach a verdict for head %s: %s", v.HeadSHA, v.Evidence))
		}
		return nil
	})
	if err == nil && revise {
		h.pushIntegrationRevision(child)
	}
	writeEpicResult(w, http.StatusCreated, v, err)
}

// integrateCheckEpicNode is the shem side of the merge lane's gate: the
// shem that finished a child asks whether it may merge the tested head into
// the run's target ref, and the answer carries the per-run slot that
// serializes those merges.
func (h *Handlers) integrateCheckEpicNode(w http.ResponseWriter, r *http.Request) {
	var body struct {
		HeadSHA string `json:"head_sha"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	child, ok := h.epicChildForShem(w, r)
	if !ok {
		return
	}
	shem := auth.ShemFromRequest(r)
	g, err := epics.GateIntegration(h.DB, r.PathValue("id"), r.PathValue("key"), body.HeadSHA, shem.ID, time.Now())
	if err != nil {
		writeEpicResult(w, 0, nil, err)
		return
	}
	resp := map[string]any{"outcome": string(g.Outcome)}
	switch g.Outcome {
	case epics.GateGranted:
		resp["target_ref"] = g.TargetRef
		resp["lease_id"] = g.LeaseID
		resp["expires_at"] = g.ExpiresAt.UTC().Format(time.RFC3339)
	case epics.GateWait:
		resp["reason"] = g.Reason
		resp["retry_after_secs"] = int(g.RetryAfter.Seconds())
	case epics.GateDone:
		resp["merge_sha"] = child.EpicIntegratedSHA
	}
	writeEpicResult(w, http.StatusOK, resp, nil)
}

// integrateEpicNodeResult is the receipt end of the lane: the shem reports
// what its merge into the target ref did, pinned to the head the gate
// granted. A merge (or a head already contained) records the integration
// receipt; a conflict sends the child back to revising with the conflicted
// files as feedback; a moved head frees the slot for a fresh gate check;
// anything else parks the child for a human.
func (h *Handlers) integrateEpicNodeResult(w http.ResponseWriter, r *http.Request) {
	var body struct {
		LocalOnly bool   `json:"local_only"`
		LeaseID   string `json:"lease_id"`
		HeadSHA   string `json:"head_sha"`
		Status    string `json:"status"`
		MergeSHA  string `json:"merge_sha"`
		Detail    string `json:"detail"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	child, ok := h.epicChildForShem(w, r)
	if !ok {
		return
	}
	switch body.Status {
	case "merged", "contained":
		if body.MergeSHA == "" {
			http.Error(w, "merge_sha is required", http.StatusUnprocessableEntity)
			return
		}
	case "conflict", "moved", "error":
		body.MergeSHA = ""
	default:
		http.Error(w, "unknown integration status", http.StatusUnprocessableEntity)
		return
	}
	pin := db.EpicIntegrationSlot{EpicID: r.PathValue("id"), NodeKey: r.PathValue("key"), HeadSHA: body.HeadSHA, LeaseID: body.LeaseID, ShemID: auth.ShemFromRequest(r).ID}
	revise := false
	err := epics.ApplyIntegrationResult(h.DB, pin, body.MergeSHA, func(tx *gorm.DB, fresh db.Ticket) error {
		scoped := *h
		scoped.DB = tx
		switch body.Status {
		case "merged", "contained":
			if body.LocalOnly {
				return epics.RecordIntegration(tx, pin.EpicID, pin.NodeKey, body.MergeSHA)
			}
			return epics.RecordIntegration(tx, pin.EpicID, pin.NodeKey, body.MergeSHA, ghsync.EnqueueFinalPR)
		case "conflict":
			feedback := fmt.Sprintf("Merging this branch into the run's integration branch conflicts. Conflicted files:\n\n%s\n\nMerge the integration branch into the existing branch, resolve every conflicted file keeping both sides' intent, commit the merge, and push. Do not rebase and do not start the ticket over.", body.Detail)
			var err error
			revise, err = scoped.bounceIntegrationTx(tx, fresh, body.HeadSHA, feedback)
			return err
		case "error":
			return scoped.parkEpicChild(fresh, "Integrating this branch into the run's integration branch failed: "+body.Detail)
		default: // moved: the lease is consumed; polling retries the new head.
			return nil
		}
	})
	if err == nil && revise {
		h.pushIntegrationRevision(child)
	}
	writeEpicResult(w, 0, nil, err)
}

// epicChildForShem loads the child the request names and checks the calling
// shem holds it — the same rule the verdict endpoint follows.
func (h *Handlers) epicChildForShem(w http.ResponseWriter, r *http.Request) (db.Ticket, bool) {
	var child db.Ticket
	if err := h.DB.Where("epic_id = ? AND epic_node_key = ?", r.PathValue("id"), r.PathValue("key")).
		First(&child).Error; err != nil {
		writeEpicResult(w, 0, nil, epics.ErrNotFound)
		return child, false
	}
	return child, h.writeTicketOwnership(w, r, child.ID)
}

// bounceIntegration sends a child whose integration failed as code back to
// revising with the failure as feedback — the same transition a human's
// request-changes performs, with the same attempt cap the add-on's monitor
// applies, so an unfixable conflict cannot loop an agent forever.
func (h *Handlers) bounceIntegrationTx(tx *gorm.DB, child db.Ticket, headSHA, feedback string) (bool, error) {
	scoped := *h
	scoped.DB = tx
	if max := config.MaxFixAttempts(); max != config.UnlimitedPRFixAttempts && child.PRFixAttempts >= max {
		return false, scoped.parkEpicChild(child, fmt.Sprintf("Stopping after %d automatic fix attempt(s) — the limit set by GOLEM_FIX_ATTEMPTS. The branch still needs work:\n\n%s", child.PRFixAttempts, feedback))
	}
	if child.AssignedShem == nil {
		return false, scoped.parkEpicChild(child, "No shem is assigned, so this cannot be fixed automatically.\n\n"+feedback)
	}
	res := tx.Model(&db.Ticket{}).Where("id = ? AND phase = ? AND epic_integrated_sha = ?", child.ID, "ready-for-review", "").Updates(map[string]any{
		"phase": "revising", "pr_fix_attempts": gorm.Expr("pr_fix_attempts + 1"), "pr_dispatched_sha": headSHA,
	})
	if res.Error != nil || res.RowsAffected == 0 {
		return false, res.Error
	}
	if err := tx.Create(&db.HumanInput{TicketID: child.ID, Kind: "feedback", Prompt: feedback, CreatedAt: time.Now()}).Error; err != nil {
		return false, err
	}
	if err := appendLogTx(tx, child.ID, "HUMAN_FEEDBACK", "orchestrator", "developer", feedback); err != nil {
		return false, err
	}
	var fresh db.Ticket
	if err := tx.First(&fresh, "id = ?", child.ID).Error; err != nil {
		return false, err
	}
	return true, enqueueGitHubPhase(tx, fresh, "revising", h.BaseURL)
}

func (h *Handlers) pushIntegrationRevision(child db.Ticket) {
	h.Hub.Push(*child.AssignedShem, ws.WSMessage{Type: "ticket_revise", TicketID: &child.ID, Repo: child.RepoRemote}) //nolint:errcheck
}

// parkEpicChild moves a child to needs-attention with the reason on the
// activity log: the integration lane could not land it and a human has to
// look.
func (h *Handlers) parkEpicChild(child db.Ticket, message string) error {
	return h.DB.Transaction(func(tx *gorm.DB) error {
		result := tx.Model(&db.Ticket{}).
			Where("id = ? AND phase = ? AND epic_integrated_sha = ?", child.ID, "ready-for-review", "").
			Update("phase", "needs-attention")
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return nil
		}
		if err := appendLogTx(tx, child.ID, "STATUS", "orchestrator", "", message); err != nil {
			return err
		}
		var fresh db.Ticket
		if err := tx.First(&fresh, "id = ?", child.ID).Error; err != nil {
			return err
		}
		return enqueueGitHubPhase(tx, fresh, "needs-attention", h.BaseURL)
	})
}
