package ui

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"

	"github.com/leonp92/golem/internal/epic"
	"github.com/leonp92/golem/internal/orchestrator/auth"
	"github.com/leonp92/golem/internal/orchestrator/db"
	"github.com/leonp92/golem/internal/orchestrator/epics"
	"github.com/leonp92/golem/internal/orchestrator/urlnorm"
)

// Epics list, New Epic and epic detail pages.

func (h *Handlers) epicsList(w http.ResponseWriter, r *http.Request) {
	repo, state := r.URL.Query().Get("repo"), r.URL.Query().Get("state")
	list, err := epics.List(h.DB, repo, state)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	data := h.base(r, "epics")
	data["Epics"] = list
	data["RepoFilter"] = repo
	data["StateFilter"] = state
	data["States"] = epics.States
	data["AvailableRepos"] = h.shemsRepos()
	h.render(w, r, "epics", data)
}

type epicForm struct {
	RepoRemote        string
	BaseBranch        string
	Title             string
	SpecMarkdown      string
	BranchStrategy    string
	DeliveryTarget    string
	AutoMergeChildren bool
	AutoApprovePhases bool
}

func (h *Handlers) renderEpicNew(w http.ResponseWriter, r *http.Request, form epicForm, errMsg string) {
	data := h.base(r, "epics")
	data["Form"] = form
	data["Error"] = errMsg
	data["AvailableRepos"] = h.shemsRepos()
	h.render(w, r, "epic_new", data)
}

func (h *Handlers) epicNewForm(w http.ResponseWriter, r *http.Request) {
	h.renderEpicNew(w, r, epicForm{BaseBranch: "main", BranchStrategy: epics.StrategyEpicBranch, DeliveryTarget: epics.TargetFeatureBranch}, "")
}

func (h *Handlers) epicNewSubmit(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	form := epicForm{
		RepoRemote:        r.FormValue("repo_remote"),
		BaseBranch:        r.FormValue("base_branch"),
		Title:             r.FormValue("title"),
		SpecMarkdown:      r.FormValue("spec_markdown"),
		BranchStrategy:    r.FormValue("branch_strategy"),
		DeliveryTarget:    r.FormValue("delivery_target"),
		AutoMergeChildren: r.FormValue("auto_merge_children") == "on",
		AutoApprovePhases: r.FormValue("auto_approve_phases") == "on",
	}
	user := auth.SessionUser(r)
	params := epics.CreateParams{
		RepoRemote:        urlnorm.Normalize(form.RepoRemote),
		Title:             form.Title,
		SpecMarkdown:      form.SpecMarkdown,
		BaseBranch:        form.BaseBranch,
		BranchStrategy:    form.BranchStrategy,
		DeliveryTarget:    form.DeliveryTarget,
		AutoMergeChildren: form.AutoMergeChildren,
		AutoApprovePhases: form.AutoApprovePhases,
	}
	if user != nil {
		params.CreatedByUserID = &user.ID
	}
	e, err := epics.Create(h.DB, params)
	if err != nil {
		h.renderEpicNew(w, r, form, err.Error())
		return
	}
	http.Redirect(w, r, fmt.Sprintf("/epics/%s", e.ID), http.StatusFound)
}

// dependencyRow is one plan node with its predecessors and child status.
type dependencyRow struct {
	Key          string
	Title        string
	BranchTarget string
	Requirements []string
	BlockedBy    []string
	Integrated   bool
	Ready        bool
	TicketID     string
	TicketPhase  string
}

func (h *Handlers) epicDetail(w http.ResponseWriter, r *http.Request) {
	var e db.Epic
	if err := h.DB.Where("id = ?", r.PathValue("id")).First(&e).Error; err != nil {
		http.NotFound(w, r)
		return
	}
	data := h.base(r, "epics")
	data["Epic"] = e
	data["Error"] = r.URL.Query().Get("error")
	if e.CurrentRevision > 0 {
		plan, rev, err := epics.LoadPlan(h.DB, e.ID, e.CurrentRevision)
		if err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		data["Revision"] = rev
		blockedBy := map[string][]string{}
		for _, edge := range plan.Edges {
			blockedBy[edge.Successor] = append(blockedBy[edge.Successor], edge.Predecessor)
		}
		var children []db.Ticket
		h.DB.Where("epic_id = ?", e.ID).Find(&children)
		byNode := map[string]db.Ticket{}
		for _, c := range children {
			byNode[c.EpicNodeKey] = c
		}
		rows := make([]dependencyRow, 0, len(plan.Nodes))
		integrated := 0
		for _, n := range plan.Nodes {
			c, ok := byNode[n.Key]
			row := dependencyRow{
				Key: n.Key, Title: n.Title, BranchTarget: n.BranchTarget,
				Requirements: n.RequirementIDs, BlockedBy: blockedBy[n.Key],
				Integrated: c.EpicIntegratedSHA != "", Ready: c.EpicReady,
			}
			if ok {
				row.TicketID, row.TicketPhase = c.ID, c.Phase
			}
			if row.Integrated {
				integrated++
			}
			rows = append(rows, row)
		}
		data["Rows"] = rows
		data["Requirements"] = plan.Requirements
		data["Integrated"] = integrated
		data["Total"] = len(rows)
	}
	h.render(w, r, "epic_detail", data)
}

// epicAction runs one epic form action and redirects back to the run,
// carrying any refusal in ?error=.
func (h *Handlers) epicAction(fn func(r *http.Request, id string) error) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		target := "/epics/" + id
		if err := r.ParseForm(); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		if err := fn(r, id); err != nil {
			target += "?error=" + url.QueryEscape(err.Error())
		}
		http.Redirect(w, r, target, http.StatusFound)
	}
}

func (h *Handlers) epicRevisionSubmit(r *http.Request, id string) error {
	var plan epic.Plan
	if err := json.Unmarshal([]byte(r.FormValue("plan_json")), &plan); err != nil {
		return fmt.Errorf("plan JSON does not parse: %w", err)
	}
	_, err := epics.AddRevision(h.DB, id, plan, r.FormValue("repo_sha"))
	return err
}

// epicApproveSubmit approves the revision and hash the page rendered.
func (h *Handlers) epicApproveSubmit(r *http.Request, id string) error {
	revision, _ := strconv.Atoi(r.FormValue("revision"))
	actor := ""
	if user := auth.SessionUser(r); user != nil {
		actor = user.Username
	}
	return epics.Approve(h.DB, id, revision, r.FormValue("plan_hash"), actor)
}

func (h *Handlers) epicStartSubmit(r *http.Request, id string) error {
	_, err := epics.Start(h.DB, id)
	return err
}

// epicMergeFinalSubmit records the human final merge: the operator attests
// the integration branch is on base and names the resulting head SHA.
func (h *Handlers) epicMergeFinalSubmit(r *http.Request, id string) error {
	return epics.CompleteFinal(h.DB, id, r.FormValue("sha"))
}

func (h *Handlers) epicPauseSubmit(r *http.Request, id string) error  { return epics.Pause(h.DB, id) }
func (h *Handlers) epicResumeSubmit(r *http.Request, id string) error { return epics.Resume(h.DB, id) }
