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
	"gorm.io/gorm"
)

// Epics list, New Epic and epic detail. These pages manage plans, never
// execution: the approve button binds a human to an exact plan hash, and
// nothing here can dispatch work (that arrives with the run loop).

func (h *Handlers) epicsList(w http.ResponseWriter, r *http.Request) {
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
	data := h.base(r, "epics")
	data["Epics"] = list
	data["RepoFilter"] = r.URL.Query().Get("repo")
	data["StateFilter"] = r.URL.Query().Get("state")
	// The run states from the design: draft through cancelled, shown
	// distinctly rather than folded into ticket phases.
	data["States"] = []string{"draft", "planning", "awaiting-approval", "running", "paused",
		"needs-attention", "awaiting-final-review", "completed", "cancelled"}
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
	AutoMergeFinal    bool
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
		AutoMergeFinal:    r.FormValue("auto_merge_final") == "on",
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
		AutoMergeFinal:    form.AutoMergeFinal,
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

// dependencyRow is one node with its hard predecessors listed in words —
// the phone-readable primary view of the DAG (the wide graph is optional).
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

// graphNode is one card in the layered dependency map: the same plan the
// table renders, laid out by depth so the critical path reads left to
// right.
type graphNode struct {
	Key        string
	Title      string
	BlockedBy  []string
	Integrated bool
	Ready      bool
	TicketID   string
}

// graphLayers buckets node keys by longest-path depth from the sources:
// layer 0 is ready work, layer k depends only on layers < k.
func graphLayers(plan *epic.Plan) [][]string {
	depth := map[string]int{}
	var resolve func(key string, seen map[string]bool) int
	resolve = func(key string, seen map[string]bool) int {
		if d, ok := depth[key]; ok {
			return d
		}
		if seen[key] {
			return 0 // validated acyclic at intake; never recurse forever
		}
		seen[key] = true
		d := 0
		for _, e := range plan.Edges {
			if e.Successor == key {
				if pd := resolve(e.Predecessor, seen) + 1; pd > d {
					d = pd
				}
			}
		}
		depth[key] = d
		return d
	}
	var layers [][]string
	for _, n := range plan.Nodes {
		d := resolve(n.Key, map[string]bool{})
		for len(layers) <= d {
			layers = append(layers, []string{})
		}
		layers[d] = append(layers[d], n.Key)
	}
	return layers
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
		integrated := map[string]bool{}
		var nodes []db.EpicNode
		h.DB.Where("epic_id = ? AND revision = ?", e.ID, e.CurrentRevision).Find(&nodes)
		for _, n := range nodes {
			integrated[n.NodeKey] = n.IntegratedSHA != ""
		}
		// Child tickets by node key, for status and links.
		var children []db.Ticket
		h.DB.Where("epic_id = ?", e.ID).Find(&children)
		byNode := map[string]db.Ticket{}
		for _, c := range children {
			byNode[c.EpicNodeKey] = c
		}
		var rows []dependencyRow
		for _, n := range plan.Nodes {
			row := dependencyRow{
				Key: n.Key, Title: n.Title, BranchTarget: n.BranchTarget,
				Requirements: n.RequirementIDs, BlockedBy: blockedBy[n.Key],
				Integrated: integrated[n.Key],
			}
			if c, ok := byNode[n.Key]; ok {
				row.TicketID = c.ID
				row.TicketPhase = c.Phase
				row.Ready = c.EpicReady
			}
			rows = append(rows, row)
		}
		data["Rows"] = rows
		data["Requirements"] = plan.Requirements

		// Run progress: receipts are what count — a closed ticket is not
		// integrated work.
		integratedCount := 0
		for _, r := range rows {
			if r.Integrated {
				integratedCount++
			}
		}
		data["Integrated"] = integratedCount
		data["Total"] = len(rows)

		// The layered dependency map (the optional wide view; the table
		// above stays the phone-readable primary).
		nodeByKey := map[string]dependencyRow{}
		for _, r := range rows {
			nodeByKey[r.Key] = r
		}
		var layers [][]graphNode
		for _, keys := range graphLayers(plan) {
			var layer []graphNode
			for _, k := range keys {
				r := nodeByKey[k]
				layer = append(layer, graphNode{Key: r.Key, Title: r.Title,
					BlockedBy: r.BlockedBy, Integrated: r.Integrated, Ready: r.Ready,
					TicketID: r.TicketID})
			}
			layers = append(layers, layer)
		}
		data["GraphLayers"] = layers
	}
	h.render(w, r, "epic_detail", data)
}

// epicRevisionSubmit accepts a candidate plan as a pasted JSON document.
// The planner job will produce these later; in M1 a human writes or edits
// one. Validation errors come back in full on the page.
func (h *Handlers) epicRevisionSubmit(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	var plan epic.Plan
	if err := json.Unmarshal([]byte(r.FormValue("plan_json")), &plan); err != nil {
		http.Redirect(w, r, "/epics/"+id+"?error="+url.QueryEscape("plan JSON does not parse: "+err.Error()), http.StatusFound)
		return
	}
	if _, err := epics.AddRevision(h.DB, id, plan, r.FormValue("repo_sha")); err != nil {
		http.Redirect(w, r, "/epics/"+id+"?error="+url.QueryEscape(err.Error()), http.StatusFound)
		return
	}
	http.Redirect(w, r, "/epics/"+id, http.StatusFound)
}

// epicApproveSubmit binds the human's approval to the revision and hash
// posted from the page, which renders them from the stored record — the
// human approves what they were shown, not a number they typed.
func (h *Handlers) epicApproveSubmit(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	revision, _ := strconv.Atoi(r.FormValue("revision"))
	actor := ""
	if user := auth.SessionUser(r); user != nil {
		actor = user.Username
	}
	if err := epics.Approve(h.DB, id, revision, r.FormValue("plan_hash"), actor); err != nil {
		http.Redirect(w, r, "/epics/"+id+"?error="+url.QueryEscape(err.Error()), http.StatusFound)
		return
	}
	http.Redirect(w, r, "/epics/"+id, http.StatusFound)
}

// epicStartSubmit runs "Approve and start"'s second half: materialize the
// approved plan into child tickets. Safe to click twice — Start is
// idempotent by construction.
func (h *Handlers) epicStartSubmit(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, err := epics.Start(h.DB, id); err != nil {
		http.Redirect(w, r, "/epics/"+id+"?error="+url.QueryEscape(err.Error()), http.StatusFound)
		return
	}
	http.Redirect(w, r, "/epics/"+id, http.StatusFound)
}

// epicMergeFinalSubmit is the button on the run view; the guards all live
// in epics.MergeFinal, and its refusal is shown back on the page.
func (h *Handlers) epicMergeFinalSubmit(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if h.GH == nil {
		http.Redirect(w, r, "/epics/"+id+"?error="+url.QueryEscape("github sync is not configured"), http.StatusFound)
		return
	}
	if err := epics.MergeFinal(r.Context(), h.DB, h.GH, id); err != nil {
		http.Redirect(w, r, "/epics/"+id+"?error="+url.QueryEscape(err.Error()), http.StatusFound)
		return
	}
	http.Redirect(w, r, "/epics/"+id, http.StatusFound)
}

func (h *Handlers) epicPauseSubmit(w http.ResponseWriter, r *http.Request) {
	h.epicStateSubmit(w, r, epics.Pause)
}

func (h *Handlers) epicResumeSubmit(w http.ResponseWriter, r *http.Request) {
	h.epicStateSubmit(w, r, epics.Resume)
}

func (h *Handlers) epicStateSubmit(w http.ResponseWriter, r *http.Request, fn func(*gorm.DB, string) error) {
	id := r.PathValue("id")
	if err := fn(h.DB, id); err != nil {
		http.Redirect(w, r, "/epics/"+id+"?error="+url.QueryEscape(err.Error()), http.StatusFound)
		return
	}
	http.Redirect(w, r, "/epics/"+id, http.StatusFound)
}
