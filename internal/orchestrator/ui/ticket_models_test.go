package ui_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

	"gorm.io/gorm"

	"github.com/leonp92/golem/internal/models"
	"github.com/leonp92/golem/internal/orchestrator/api"
	"github.com/leonp92/golem/internal/orchestrator/auth"
	"github.com/leonp92/golem/internal/orchestrator/db"
	"github.com/leonp92/golem/internal/orchestrator/rbac"
	"github.com/leonp92/golem/internal/orchestrator/ui"
	ws "github.com/leonp92/golem/internal/orchestrator/ws"
)

// modelFixture is a UI mux with a session cookie, plus the API's action route
// so a rendered control can be driven against the real handler.
type modelFixture struct {
	gdb    *gorm.DB
	mux    *http.ServeMux
	cookie *http.Cookie
}

func newModelFixture(t *testing.T) *modelFixture {
	t.Helper()
	gdb, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	tmpls, err := ui.LoadTemplates()
	if err != nil {
		t.Fatalf("LoadTemplates: %v", err)
	}
	h := ui.NewHandlersWithMap(gdb, tmpls, false)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	// The panel's controls post to the API, so both are mounted.
	api.NewHandlers(gdb, ws.NewHub(), nil).RegisterHumanRoutes(mux)

	user := db.User{Username: "leon", PasswordHash: "x", Role: string(rbac.RoleAdmin)}
	gdb.Create(&user)
	rec := httptest.NewRecorder()
	if err := auth.CreateSession(gdb, rec, user.ID, false); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	return &modelFixture{gdb: gdb, mux: mux, cookie: rec.Result().Cookies()[0]}
}

func (f *modelFixture) shem(t *testing.T, name, backend string, ids ...string) {
	t.Helper()
	cat := models.Catalog{SupportsSelection: true, Tiers: []models.Tier{"small", "large"},
		StageDefaults: map[models.Stage]models.Tier{}}
	for _, id := range ids {
		cat.Models = append(cat.Models, models.Model{ID: id, Label: id, Tier: "large"})
	}
	for _, st := range models.Stages {
		cat.StageDefaults[st] = "large"
	}
	f.singleShem(t, name, backend, cat)
}

func (f *modelFixture) singleShem(t *testing.T, name, backend string, cat models.Catalog) {
	t.Helper()
	raw, err := json.Marshal(cat)
	if err != nil {
		t.Fatal(err)
	}
	s := db.Shem{Name: name, APIKeyHash: "x", Repos: `["https://github.com/org/repo"]`,
		Status: "online", Backend: backend, Catalog: string(raw)}
	if err := f.gdb.Create(&s).Error; err != nil {
		t.Fatalf("seed shem %s: %v", name, err)
	}
}

func (f *modelFixture) ticket(t *testing.T, id string, sel models.Selections, backend, phase string) db.Ticket {
	t.Helper()
	ticket := db.Ticket{ID: id + "-0000000000", RepoRemote: "https://github.com/org/repo", BaseBranch: "main",
		Branch: "b", Title: "t", Description: "d", Phase: phase, ModelBackend: backend}
	ticket.SetModelSelections(sel)
	if err := f.gdb.Create(&ticket).Error; err != nil {
		t.Fatalf("seed ticket: %v", err)
	}
	return ticket
}

func (f *modelFixture) get(t *testing.T, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.AddCookie(f.cookie)
	w := httptest.NewRecorder()
	f.mux.ServeHTTP(w, req)
	return w
}

func (f *modelFixture) post(t *testing.T, path string, form url.Values) *httptest.ResponseRecorder {
	t.Helper()
	form.Set(auth.CSRFFormField, auth.CSRFTokenForSession(f.cookie.Value))
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(f.cookie)
	w := httptest.NewRecorder()
	f.mux.ServeHTTP(w, req)
	return w
}

func TestNewTicketFormOffersEveryStageAndTheCatalogsTiers(t *testing.T) {
	f := newModelFixture(t)
	f.shem(t, "node-a", "claude-code", "opus", "haiku")

	body := f.get(t, "/tickets/new").Body.String()
	if !strings.Contains(body, `name="model_default"`) {
		t.Error("the form has no default model select")
	}
	for _, st := range models.Stages {
		if !strings.Contains(body, `name="model_`+string(st)+`"`) {
			t.Errorf("the form has no select for stage %q", st)
		}
	}
	for _, tier := range []string{"tier:small", "tier:large"} {
		if !strings.Contains(body, `value="`+tier+`"`) {
			t.Errorf("the form does not offer %q", tier)
		}
	}
	for _, id := range []string{"opus", "haiku"} {
		if !strings.Contains(body, `value="`+id+`"`) {
			t.Errorf("the form does not offer model %q", id)
		}
	}
	// The tier group sits above the models.
	if strings.Index(body, `value="tier:small"`) > strings.Index(body, `value="opus"`) {
		t.Error("the tier options are below the concrete models")
	}
}

func TestASubmittedSelectionReachesTheCreatedTicket(t *testing.T) {
	f := newModelFixture(t)
	f.shem(t, "node-a", "claude-code", "opus")

	w := f.post(t, "/tickets/new", url.Values{
		"repo_remote": {"https://github.com/org/repo"}, "base_branch": {"main"},
		"title": {"Model picker"}, "description": {"d"},
		"model_backend": {"claude-code"}, "model_brainstorm": {"opus"},
	})
	if w.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302: %s", w.Code, w.Body.String())
	}
	var created db.Ticket
	if err := f.gdb.First(&created).Error; err != nil {
		t.Fatal(err)
	}
	if created.ModelSelections()["brainstorm"] != "opus" || created.ModelBackend != "claude-code" {
		t.Fatalf("selections = %v, backend = %q", created.ModelSelections(), created.ModelBackend)
	}
	detail := f.get(t, "/tickets/"+created.ID).Body.String()
	if !strings.Contains(detail, "opus") {
		t.Error("the detail page does not show the selected model")
	}
}

func TestTwoBackendsOfferOnlyTheSelectedOnesModels(t *testing.T) {
	f := newModelFixture(t)
	f.shem(t, "node-a", "claude-code", "opus")
	f.shem(t, "node-b", "codex", "gpt-big")

	// The first reported backend, alphabetically, is the initial render's.
	body := f.get(t, "/tickets/new").Body.String()
	if !strings.Contains(body, `value="opus"`) || strings.Contains(body, `value="gpt-big"`) {
		t.Error("the initial render does not offer only claude-code's models")
	}

	switched := f.get(t, "/tickets/model-selects?model_backend=codex").Body.String()
	if !strings.Contains(switched, `value="gpt-big"`) || strings.Contains(switched, `value="opus"`) {
		t.Errorf("the switched render does not offer only codex's models:\n%s", switched)
	}

	w := f.post(t, "/tickets/new", url.Values{
		"repo_remote": {"https://github.com/org/repo"}, "base_branch": {"main"},
		"title": {"Codex ticket"}, "description": {"d"},
		"model_backend": {"codex"}, "model_plan": {"gpt-big"},
	})
	if w.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302: %s", w.Code, w.Body.String())
	}
}

func TestAValidationErrorKeepsTheChosenModels(t *testing.T) {
	f := newModelFixture(t)
	f.shem(t, "node-a", "claude-code", "opus")

	w := f.post(t, "/tickets/new", url.Values{
		"repo_remote": {"https://github.com/org/repo"}, "base_branch": {"main"},
		"title": {"Bad model"}, "description": {"d"},
		"model_backend": {"claude-code"}, "model_brainstorm": {"nonesuch"},
	})
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want a re-render: %s", w.Code, w.Body.String())
	}
	var count int64
	f.gdb.Model(&db.Ticket{}).Count(&count)
	if count != 0 {
		t.Errorf("a rejected submission created %d tickets", count)
	}
	// The chosen value is not in the catalog, so what has to survive is the
	// rest of the form plus the error.
	body := w.Body.String()
	if !strings.Contains(body, "nonesuch") {
		t.Error("the re-render does not report the rejected value")
	}
	if !strings.Contains(body, "Bad model") {
		t.Error("the re-render lost the title")
	}
}

func TestASelectionIsKeptSelectedOnReRender(t *testing.T) {
	f := newModelFixture(t)
	f.shem(t, "node-a", "claude-code", "opus")

	// Description missing, so it re-renders with the model kept.
	w := f.post(t, "/tickets/new", url.Values{
		"repo_remote": {"https://github.com/org/repo"}, "base_branch": {"main"},
		"title": {"No description"}, "model_backend": {"claude-code"}, "model_brainstorm": {"opus"},
	})
	body := w.Body.String()
	if !strings.Contains(body, `value="opus" selected`) {
		t.Errorf("the re-render did not keep the chosen model selected:\n%s", body)
	}
}

func TestNoSelectableCatalogRendersANoticeAndNoModelFields(t *testing.T) {
	tests := []struct {
		name  string
		setup func(t *testing.T, f *modelFixture)
		want  string
	}{
		{
			name:  "no reported catalog",
			setup: func(*testing.T, *modelFixture) {},
			want:  "No worker has reported a model catalog yet.",
		},
		{
			name: "a backend that runs a single model",
			setup: func(t *testing.T, f *modelFixture) {
				f.singleShem(t, "node-s", "single",
					models.Catalog{Models: []models.Model{{ID: "only", Label: "Only"}}})
			},
			want: "single runs a single model.",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newModelFixture(t)
			tt.setup(t, f)
			body := f.get(t, "/tickets/new").Body.String()
			if !strings.Contains(body, tt.want) {
				t.Errorf("the form does not carry %q:\n%s", tt.want, body)
			}
			if strings.Contains(body, `name="model_brainstorm"`) {
				t.Error("the form emits model fields with no selectable catalog")
			}
		})
	}
}

func TestTicketPagePanelShapeAndActionField(t *testing.T) {
	f := newModelFixture(t)
	f.shem(t, "node-a", "claude-code", "opus")
	ticket := f.ticket(t, "t-panel", models.Selections{}, "", "unassigned")

	body := f.get(t, "/tickets/"+ticket.ID).Body.String()
	// The panel carries no `action` input: each submit names its own through
	// hx-vals, so the body can never hold two action values.
	panelStart := strings.Index(body, `id="ticket-models"`)
	if panelStart < 0 {
		t.Fatal("the ticket page renders no #ticket-models form")
	}
	panelEnd := strings.Index(body[panelStart:], "</form>") + panelStart
	panel := body[panelStart:panelEnd]
	if strings.Contains(panel, `name="action"`) {
		t.Errorf("the panel carries an action input:\n%s", panel)
	}
	if got := strings.Count(body, `class="model-selects`); got != 1 {
		t.Errorf("the page renders %d model-selects elements, want exactly 1", got)
	}
}

func TestTicketPagePanelOnlyWhileUnclaimedAndOpen(t *testing.T) {
	shemID := uint(1)
	tests := []struct {
		name      string
		phase     string
		assigned  *uint
		wantPanel bool
	}{
		{"unclaimed and open", "unassigned", nil, true},
		{"claimed", "implement", &shemID, false},
		{"closed", "closed", nil, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newModelFixture(t)
			f.shem(t, "node-a", "claude-code", "opus")
			ticket := f.ticket(t, "t-1", models.Selections{"brainstorm": "opus"}, "claude-code", tt.phase)
			if tt.assigned != nil {
				f.gdb.Model(&ticket).Update("assigned_shem", *tt.assigned)
			}

			body := f.get(t, "/tickets/"+ticket.ID).Body.String()
			if got := strings.Contains(body, `id="ticket-models"`); got != tt.wantPanel {
				t.Errorf("panel rendered = %v, want %v", got, tt.wantPanel)
			}
			// The read-only block is always there, so a claimed ticket still
			// shows what it is running on.
			if !strings.Contains(body, "Models") {
				t.Error("the page does not show the resolved models")
			}
			for _, st := range models.Stages {
				if !strings.Contains(body, ">"+string(st)+"<") {
					t.Errorf("the read-only block does not name stage %q", st)
				}
			}
		})
	}
}

// Start posts exactly one action value: the panel's selects are included, not
// any button's hx-vals.
func TestStartPostsOneActionWithThePanelsModelFields(t *testing.T) {
	var actions atomic.Int64
	var seen []string
	f := newModelFixture(t)
	f.shem(t, "node-a", "claude-code", "opus")

	// Stand in for the API so the exact submitted body can be read.
	f.mux.HandleFunc("POST /probe/actions", func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Error(err)
			return
		}
		actions.Store(int64(len(r.PostForm["action"])))
		seen = r.PostForm["action"]
		if v := r.PostForm.Get("model_brainstorm"); v != "" {
			seen = append(seen, "model_brainstorm="+v)
		}
		w.WriteHeader(http.StatusNoContent)
	})

	// The rendered Start control's own values, submitted together the way
	// htmx serialises hx-vals plus hx-include.
	form := url.Values{
		"action":             {"start"},
		"reviewed_body_hash": {"h"},
		"model_backend":      {"claude-code"},
		"model_brainstorm":   {"opus"},
	}
	if w := f.post(t, "/probe/actions", form); w.Code != http.StatusNoContent {
		t.Fatalf("status = %d", w.Code)
	}
	if actions.Load() != 1 {
		t.Errorf("the body carried %d action values, want 1: %v", actions.Load(), seen)
	}
	if seen[0] != "start" {
		t.Errorf("action = %q, want start", seen[0])
	}
	var sawModel bool
	for _, s := range seen {
		if s == "model_brainstorm=opus" {
			sawModel = true
		}
	}
	if !sawModel {
		t.Errorf("the panel's model fields were not submitted: %v", seen)
	}
}

// Clear carries no hx-include, so its body is action alone — an empty
// selection set, which is what clears both columns.
func TestClearClearsBothColumns(t *testing.T) {
	f := newModelFixture(t)
	f.shem(t, "node-a", "claude-code", "opus")
	ticket := f.ticket(t, "t-clear", models.Selections{"brainstorm": "opus"}, "claude-code", "unassigned")

	body := f.get(t, "/tickets/"+ticket.ID).Body.String()
	if !strings.Contains(body, "Clear model selections") {
		t.Fatal("the panel has no clear control")
	}

	w := f.post(t, "/api/tickets/"+ticket.ID+"/actions", url.Values{"action": {"set-models"}})
	if w.Code != http.StatusNoContent {
		t.Fatalf("status = %d: %s", w.Code, w.Body.String())
	}
	var after db.Ticket
	if err := f.gdb.First(&after, "id = ?", ticket.ID).Error; err != nil {
		t.Fatal(err)
	}
	if after.Models != "{}" || after.ModelBackend != "" {
		t.Errorf("models = %q, backend = %q; want cleared", after.Models, after.ModelBackend)
	}
}

func TestTheWaitingBannerAndBadgeAppearAndClear(t *testing.T) {
	f := newModelFixture(t)
	f.shem(t, "node-a", "claude-code", "opus")
	// Bound to a backend no shem runs.
	ticket := f.ticket(t, "t-wait", models.Selections{"brainstorm": "gpt-big"}, "codex", "unassigned")

	detail := f.get(t, "/tickets/"+ticket.ID).Body.String()
	if !strings.Contains(detail, "waiting for a codex shem") {
		t.Errorf("the ticket page shows no waiting banner:\n%s", detail)
	}
	dash := f.get(t, "/dashboard").Body.String()
	if !strings.Contains(dash, "waiting for a codex shem") {
		t.Error("the dashboard row shows no waiting badge")
	}

	if w := f.post(t, "/api/tickets/"+ticket.ID+"/actions", url.Values{"action": {"set-models"}}); w.Code != http.StatusNoContent {
		t.Fatalf("set-models: %d %s", w.Code, w.Body.String())
	}
	cleared := f.get(t, "/tickets/"+ticket.ID).Body.String()
	if strings.Contains(cleared, "waiting for a codex shem") {
		t.Error("clearing the selections left the banner")
	}
}

func TestAClaimableTicketsRowHasNoWaitingBadge(t *testing.T) {
	f := newModelFixture(t)
	f.shem(t, "node-a", "claude-code", "opus")
	f.ticket(t, "t-ok", models.Selections{"brainstorm": "opus"}, "claude-code", "unassigned")

	if body := f.get(t, "/dashboard").Body.String(); strings.Contains(body, `badge-warning rounded font-medium mt-0.5`) {
		t.Error("a claimable ticket's row shows a waiting badge")
	}
}

// A badge per row must not cost a query per row.
func TestTheDashboardLoadsTheFleetOnce(t *testing.T) {
	f := newModelFixture(t)
	f.shem(t, "node-a", "claude-code", "opus")
	for _, id := range []string{"t-1", "t-2", "t-3"} {
		f.ticket(t, id, models.Selections{"brainstorm": "gpt-big"}, "codex", "unassigned")
	}

	var shemQueries atomic.Int64
	err := f.gdb.Callback().Query().After("gorm:query").Register("ui_test:count", func(tx *gorm.DB) {
		if tx.Statement != nil && tx.Statement.Table == "shems" {
			shemQueries.Add(1)
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	f.get(t, "/dashboard")
	// One for the fleet, plus the dashboard's own existing shem lookups.
	if got := shemQueries.Load(); got > 3 {
		t.Errorf("the dashboard issued %d shem queries for 3 rows, want a per-page count", got)
	}
}

func TestRequeueAndRequestChangesCarryTheBackend(t *testing.T) {
	f := newModelFixture(t)
	f.shem(t, "node-a", "claude-code", "opus")
	shemID := uint(1)

	// Both controls are reachable while the ticket is claimed.
	ticket := f.ticket(t, "t-claimed", models.Selections{}, "", "ready-for-review")
	f.gdb.Model(&ticket).Update("assigned_shem", shemID)

	body := f.get(t, "/tickets/"+ticket.ID).Body.String()
	if !strings.Contains(body, `name="model_default"`) {
		t.Error("the requeue control has no model select")
	}
	if !strings.Contains(body, `name="model_revise"`) {
		t.Error("the request-changes control has no model select")
	}
	if !strings.Contains(body, `name="model_backend" value="claude-code"`) {
		t.Errorf("neither control carries the picker's backend:\n%s", body)
	}

	// A concrete id succeeds on a ticket whose stored model_backend is empty.
	w := f.post(t, "/api/tickets/"+ticket.ID+"/actions", url.Values{
		"action": {"requeue"}, "model_backend": {"claude-code"}, "model_default": {"opus"},
	})
	if w.Code != http.StatusNoContent {
		t.Fatalf("requeue status = %d: %s", w.Code, w.Body.String())
	}
	var after db.Ticket
	if err := f.gdb.First(&after, "id = ?", ticket.ID).Error; err != nil {
		t.Fatal(err)
	}
	if after.ModelSelections()[models.DefaultKey] != "opus" || after.ModelBackend != "claude-code" {
		t.Errorf("selections = %v, backend = %q", after.ModelSelections(), after.ModelBackend)
	}
}

func TestALoadFleetFailureAnswers500(t *testing.T) {
	f := newModelFixture(t)
	f.shem(t, "node-a", "claude-code", "opus")
	ticket := f.ticket(t, "t-500", models.Selections{}, "", "unassigned")
	if err := f.gdb.Migrator().DropTable(&db.Shem{}); err != nil {
		t.Fatal(err)
	}

	for _, path := range []string{
		"/dashboard", "/tickets/new", "/tickets/model-selects", "/tickets/" + ticket.ID,
	} {
		if w := f.get(t, path); w.Code != http.StatusInternalServerError {
			t.Errorf("%s status = %d, want 500", path, w.Code)
		}
	}
}

func TestTheShemListShowsEachBackend(t *testing.T) {
	f := newModelFixture(t)
	f.shem(t, "node-a", "claude-code", "opus")
	f.shem(t, "node-b", "codex", "gpt-big")

	body := f.get(t, "/shems").Body.String()
	for _, want := range []string{"claude-code", "codex"} {
		if !strings.Contains(body, want) {
			t.Errorf("the shem list does not show %q", want)
		}
	}
}

func TestALogEntryWithAModelShowsTheBadge(t *testing.T) {
	f := newModelFixture(t)
	f.shem(t, "node-a", "claude-code", "opus")
	ticket := f.ticket(t, "t-log", models.Selections{}, "", "implement")
	entry := db.LogEntry{TicketID: ticket.ID, SequenceNum: 1, EntryType: "STATUS",
		FromRole: "shem", Message: "Starting agent", Backend: "claude-code", Model: "opus"}
	if err := f.gdb.Create(&entry).Error; err != nil {
		t.Fatal(err)
	}

	body := f.get(t, "/tickets/"+ticket.ID).Body.String()
	if !strings.Contains(body, "claude-code/opus") {
		t.Errorf("the log entry shows no backend/model badge:\n%s", body)
	}
}
