package api

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"

	"github.com/leonp92/golem/internal/epic"
	"github.com/leonp92/golem/internal/orchestrator/auth"
	"github.com/leonp92/golem/internal/orchestrator/db"
	"github.com/leonp92/golem/internal/orchestrator/epics"
	"github.com/leonp92/golem/internal/orchestrator/rbac"
	"github.com/leonp92/golem/internal/orchestrator/urlnorm"
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
	// Shem-authenticated: the tester posts verdicts for the child it holds.
	mux.Handle("POST /api/epics/{id}/verdicts", auth.RequireAPIKey(h.DB)(http.HandlerFunc(h.postEpicVerdict)))
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
	writeEpicResult(w, 0, nil, epics.Resume(h.DB, r.PathValue("id")))
}

func (h *Handlers) mergeFinalEpic(w http.ResponseWriter, r *http.Request) {
	if h.GH == nil {
		http.Error(w, "github sync is not configured", http.StatusServiceUnavailable)
		return
	}
	writeEpicResult(w, 0, nil, epics.MergeFinal(r.Context(), h.DB, h.GH, r.PathValue("id")))
}

type createEpicRequest struct {
	RepoRemote        string `json:"repo_remote"`
	Title             string `json:"title"`
	SpecMarkdown      string `json:"spec_markdown"`
	BaseBranch        string `json:"base_branch"`
	BranchStrategy    string `json:"branch_strategy"`
	DeliveryTarget    string `json:"delivery_target"`
	AutoMergeChildren bool   `json:"auto_merge_children"`
	AutoMergeFinal    bool   `json:"auto_merge_final"`
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
		AutoMergeFinal:    body.AutoMergeFinal,
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
	writeEpicResult(w, 0, nil, epics.RecordIntegration(h.DB, r.PathValue("id"), r.PathValue("key"), body.SHA))
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
	if err := h.DB.Select("id").Where("epic_id = ? AND epic_node_key = ?", v.EpicID, v.EpicNodeKey).
		First(&child).Error; err != nil {
		http.Error(w, errNotOwner.Error(), http.StatusConflict)
		return
	}
	if !h.writeTicketOwnership(w, r, child.ID) {
		return
	}
	writeEpicResult(w, http.StatusCreated, v, epics.RecordVerdict(h.DB, v))
}
