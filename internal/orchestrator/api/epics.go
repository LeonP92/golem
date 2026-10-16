package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/leonp92/golem/internal/epic"
	"github.com/leonp92/golem/internal/orchestrator/auth"
	"github.com/leonp92/golem/internal/orchestrator/db"
	"github.com/leonp92/golem/internal/orchestrator/epics"
	"github.com/leonp92/golem/internal/orchestrator/rbac"
	"github.com/leonp92/golem/internal/orchestrator/urlnorm"
)

// RegisterEpicRoutes adds the Epic Run planning routes. Everything here is
// plans-without-execution (M1): creating epics, storing and reviewing
// immutable plan revisions, and approving them. Nothing on these routes can
// dispatch work.
func (h *Handlers) RegisterEpicRoutes(mux *http.ServeMux) {
	mux.Handle("POST /api/epics",
		auth.RequireSession(h.DB)(auth.RequireCSRF(rbac.Require(rbac.PermTicketCreate)(http.HandlerFunc(h.createEpic)))))
	mux.Handle("GET /api/epics",
		auth.RequireSession(h.DB)(rbac.Require(rbac.PermTicketView)(http.HandlerFunc(h.listEpics))))
	mux.Handle("GET /api/epics/{id}",
		auth.RequireSession(h.DB)(rbac.Require(rbac.PermTicketView)(http.HandlerFunc(h.getEpic))))
	mux.Handle("POST /api/epics/{id}/revisions",
		auth.RequireSession(h.DB)(auth.RequireCSRF(rbac.Require(rbac.PermTicketCreate)(http.HandlerFunc(h.addEpicRevision)))))
	mux.Handle("POST /api/epics/{id}/approve",
		auth.RequireSession(h.DB)(auth.RequireCSRF(rbac.Require(rbac.PermTicketManage)(http.HandlerFunc(h.approveEpic)))))
	mux.Handle("POST /api/epics/{id}/start",
		auth.RequireSession(h.DB)(auth.RequireCSRF(rbac.Require(rbac.PermTicketManage)(http.HandlerFunc(h.startEpic)))))
	mux.Handle("POST /api/epics/{id}/nodes/{key}/integrate",
		auth.RequireSession(h.DB)(auth.RequireCSRF(rbac.Require(rbac.PermTicketManage)(http.HandlerFunc(h.integrateEpicNode)))))
	// The tester runner authenticates with its shem API key, like every
	// other worker-originated write. A verdict is evidence, not an order:
	// RecordVerdict validates its shape and the lane still matches the
	// head SHA before it merges anything.
	mux.Handle("POST /api/epics/{id}/verdicts",
		auth.RequireAPIKey(h.DB)(http.HandlerFunc(h.postEpicVerdict)))
	mux.Handle("POST /api/epics/{id}/merge-final",
		auth.RequireSession(h.DB)(auth.RequireCSRF(rbac.Require(rbac.PermTicketManage)(http.HandlerFunc(h.mergeFinalEpic)))))
	mux.Handle("POST /api/epics/{id}/pause",
		auth.RequireSession(h.DB)(auth.RequireCSRF(rbac.Require(rbac.PermTicketManage)(http.HandlerFunc(h.pauseEpic)))))
	mux.Handle("POST /api/epics/{id}/resume",
		auth.RequireSession(h.DB)(auth.RequireCSRF(rbac.Require(rbac.PermTicketManage)(http.HandlerFunc(h.resumeEpic)))))
}

func (h *Handlers) pauseEpic(w http.ResponseWriter, r *http.Request) {
	writeEpicStateChange(w, epics.Pause(h.DB, r.PathValue("id")))
}

func (h *Handlers) resumeEpic(w http.ResponseWriter, r *http.Request) {
	writeEpicStateChange(w, epics.Resume(h.DB, r.PathValue("id")))
}

func writeEpicStateChange(w http.ResponseWriter, err error) {
	if err == nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if errors.Is(err, epics.ErrNotFound) {
		http.Error(w, "epic not found", http.StatusNotFound)
		return
	}
	http.Error(w, err.Error(), http.StatusConflict)
}

// mergeFinalEpic is the human's trigger for the final merge; every guard
// (delivery target, run state, tester pass, pinned head) lives in
// epics.MergeFinal so this handler and any future automation share them.
func (h *Handlers) mergeFinalEpic(w http.ResponseWriter, r *http.Request) {
	if h.GH == nil {
		http.Error(w, "github sync is not configured", http.StatusServiceUnavailable)
		return
	}
	if err := epics.MergeFinal(r.Context(), h.DB, h.GH, r.PathValue("id")); err != nil {
		if errors.Is(err, epics.ErrNotFound) {
			http.Error(w, "epic not found", http.StatusNotFound)
			return
		}
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type createEpicRequest struct {
	RepoRemote     string `json:"repo_remote"`
	Title          string `json:"title"`
	SpecMarkdown   string `json:"spec_markdown"`
	BaseBranch     string `json:"base_branch"`
	BranchStrategy string `json:"branch_strategy"`
	DeliveryTarget string `json:"delivery_target"`
	// Pointer fields: an absent key must default to false, same as an
	// explicit false — older clients sending the pre-M3 shape keep the
	// human-merged behavior.
	AutoMergeChildren *bool `json:"auto_merge_children"`
	AutoMergeFinal    *bool `json:"auto_merge_final"`
	AutoApprovePhases *bool `json:"auto_approve_phases"`
}

func (h *Handlers) createEpic(w http.ResponseWriter, r *http.Request) {
	var body createEpicRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	user := auth.SessionUser(r)
	params := epics.CreateParams{
		RepoRemote:     urlnorm.Normalize(body.RepoRemote),
		Title:          body.Title,
		SpecMarkdown:   body.SpecMarkdown,
		BaseBranch:     body.BaseBranch,
		BranchStrategy: body.BranchStrategy,
		DeliveryTarget: body.DeliveryTarget,
	}
	if body.AutoMergeChildren != nil {
		params.AutoMergeChildren = *body.AutoMergeChildren
	}
	if body.AutoMergeFinal != nil {
		params.AutoMergeFinal = *body.AutoMergeFinal
	}
	if body.AutoApprovePhases != nil {
		params.AutoApprovePhases = *body.AutoApprovePhases
	}
	if user != nil {
		params.CreatedByUserID = &user.ID
	}
	e, err := epics.Create(h.DB, params)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(e) //nolint:errcheck
}

func (h *Handlers) listEpics(w http.ResponseWriter, r *http.Request) {
	var list []db.Epic
	q := h.DB.Order("created_at desc")
	if repo := r.URL.Query().Get("repo"); repo != "" {
		q = q.Where("repo_remote = ?", urlnorm.Normalize(repo))
	}
	if state := r.URL.Query().Get("state"); state != "" {
		q = q.Where("state = ?", state)
	}
	if err := q.Find(&list).Error; err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(list) //nolint:errcheck
}

func (h *Handlers) getEpic(w http.ResponseWriter, r *http.Request) {
	var e db.Epic
	if err := h.DB.Where("id = ?", r.PathValue("id")).First(&e).Error; err != nil {
		http.Error(w, "epic not found", http.StatusNotFound)
		return
	}
	resp := map[string]any{"epic": e}
	if e.CurrentRevision > 0 {
		plan, rev, err := epics.LoadPlan(h.DB, e.ID, e.CurrentRevision)
		if err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		resp["plan"] = plan
		resp["revision"] = rev
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp) //nolint:errcheck
}

// addEpicRevision accepts a candidate plan as JSON. Validation errors are
// 422 with every problem listed: the caller (human or, later, the planner
// job) fixes one pass, not five.
func (h *Handlers) addEpicRevision(w http.ResponseWriter, r *http.Request) {
	var plan epic.Plan
	if err := json.NewDecoder(r.Body).Decode(&plan); err != nil {
		http.Error(w, "invalid plan JSON", http.StatusBadRequest)
		return
	}
	rev, err := epics.AddRevision(h.DB, r.PathValue("id"), plan, r.URL.Query().Get("repo_sha"))
	switch {
	case errors.Is(err, epics.ErrNotFound):
		http.Error(w, "epic not found", http.StatusNotFound)
	case errors.Is(err, epics.ErrNotEditable):
		http.Error(w, err.Error(), http.StatusConflict)
	case err != nil:
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
	default:
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(rev) //nolint:errcheck
	}
}

type approveEpicRequest struct {
	Revision int    `json:"revision"`
	PlanHash string `json:"plan_hash"`
}

// approveEpic binds the human's approval to an exact revision and content
// hash. The client must send both — approving "whatever is current" is not
// an approval of anything.
func (h *Handlers) approveEpic(w http.ResponseWriter, r *http.Request) {
	var body approveEpicRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	actor := ""
	if user := auth.SessionUser(r); user != nil {
		actor = strconv.FormatUint(uint64(user.ID), 10)
	}
	err := epics.Approve(h.DB, r.PathValue("id"), body.Revision, body.PlanHash, actor)
	switch {
	case errors.Is(err, epics.ErrNotFound):
		http.Error(w, "epic not found", http.StatusNotFound)
	case errors.Is(err, epics.ErrRevisionMissing):
		http.Error(w, "revision not found", http.StatusNotFound)
	case errors.Is(err, epics.ErrHashMismatch), errors.Is(err, epics.ErrNotCurrent):
		http.Error(w, err.Error(), http.StatusConflict)
	case err != nil:
		http.Error(w, "internal error", http.StatusInternalServerError)
	default:
		w.WriteHeader(http.StatusNoContent)
	}
}

// startEpic turns an approved plan into runnable child tickets. Repeated
// posts are safe and create nothing (deterministic child IDs + unique
// node mapping), which is the M2 "restart never duplicates" rule at the
// HTTP surface.
func (h *Handlers) startEpic(w http.ResponseWriter, r *http.Request) {
	created, err := epics.Start(h.DB, r.PathValue("id"))
	switch {
	case errors.Is(err, epics.ErrNotFound):
		http.Error(w, "epic not found", http.StatusNotFound)
	case errors.Is(err, epics.ErrNotApproved):
		http.Error(w, err.Error(), http.StatusConflict)
	case errors.Is(err, epics.ErrWrongState):
		http.Error(w, err.Error(), http.StatusConflict)
	case err != nil:
		http.Error(w, "internal error", http.StatusInternalServerError)
	default:
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]int{"created": created}) //nolint:errcheck
	}
}

// integrateEpicNode records the receipt that a node's code merged into its
// branch target. In M2 humans merge child PRs; for repositories without
// GitHub sync (and for tests) this endpoint is how the receipt arrives.
// Where PR sync is live, the PR monitor writes the same receipt from the
// remote merge event.
func (h *Handlers) integrateEpicNode(w http.ResponseWriter, r *http.Request) {
	var body struct {
		SHA       string `json:"sha"`
		TargetRef string `json:"target_ref"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	err := epics.RecordIntegration(h.DB, r.PathValue("id"), r.PathValue("key"), body.SHA, body.TargetRef)
	switch {
	case errors.Is(err, epics.ErrNotFound):
		http.Error(w, "epic not found", http.StatusNotFound)
	case errors.Is(err, epics.ErrWrongState):
		http.Error(w, err.Error(), http.StatusConflict)
	case errors.Is(err, epics.ErrIntegrationConflict):
		http.Error(w, err.Error(), http.StatusConflict)
	case err != nil:
		http.Error(w, err.Error(), http.StatusBadRequest)
	default:
		w.WriteHeader(http.StatusNoContent)
	}
}

type postEpicVerdictRequest struct {
	EpicNodeKey string `json:"epic_node_key"`
	HeadSHA     string `json:"head_sha"`
	BaseSHA     string `json:"base_sha"`
	Verdict     string `json:"verdict"`
	Scenarios   string `json:"scenarios"`
	Evidence    string `json:"evidence"`
	Backend     string `json:"backend"`
}

func (h *Handlers) postEpicVerdict(w http.ResponseWriter, r *http.Request) {
	var body postEpicVerdictRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	// A shem vouches only for the node ticket it holds; the final
	// candidate (empty key) is never a shem's to pass.
	if body.EpicNodeKey == "" {
		http.Error(w, "epic_node_key is required", http.StatusBadRequest)
		return
	}
	var child db.Ticket
	if err := h.DB.Select("id").Where("epic_id = ? AND epic_node_key = ?",
		r.PathValue("id"), body.EpicNodeKey).First(&child).Error; err != nil {
		http.Error(w, errNotOwner.Error(), http.StatusConflict)
		return
	}
	if !h.writeTicketOwnership(w, r, child.ID) {
		return
	}
	v := db.TesterVerdict{
		EpicID:      r.PathValue("id"),
		EpicNodeKey: body.EpicNodeKey,
		HeadSHA:     body.HeadSHA,
		BaseSHA:     body.BaseSHA,
		Verdict:     body.Verdict,
		Scenarios:   body.Scenarios,
		Evidence:    body.Evidence,
		Backend:     body.Backend,
	}
	if err := epics.RecordVerdict(h.DB, v); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(v) //nolint:errcheck
}
