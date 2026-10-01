package api_test

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/leonp92/golem/internal/models"
	"github.com/leonp92/golem/internal/orchestrator/api"
	"github.com/leonp92/golem/internal/orchestrator/auth"
	"github.com/leonp92/golem/internal/orchestrator/db"
	"github.com/leonp92/golem/internal/orchestrator/ghsync"
)

// postModelAction submits one form-encoded action with a valid CSRF token.
func postModelAction(t *testing.T, mux *http.ServeMux, cookie *http.Cookie, ticketID string, form url.Values) *httptest.ResponseRecorder {
	t.Helper()
	form.Set(auth.CSRFFormField, auth.CSRFTokenForSession(cookie.Value))
	req := httptest.NewRequest(http.MethodPost, "/api/tickets/"+ticketID+"/actions",
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(cookie)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	return w
}

// setModelsFixture is a handlers set with the human routes, a session, two
// reported backends, and one unclaimed ticket.
func setModelsFixture(t *testing.T) (*api.Handlers, *http.ServeMux, *http.Cookie, db.Ticket) {
	t.Helper()
	h, mux, cookie := setupActionTest(t)
	seedShemWithCatalog(t, h.DB, "node-a", "key1", "claude-code", modelCatalog("opus", "haiku"))
	seedShemWithCatalog(t, h.DB, "node-s", "key2", "single",
		models.Catalog{Models: []models.Model{{ID: "only"}}})
	ticket := db.Ticket{RepoRemote: "r", BaseBranch: "main", Branch: "b", Title: "t",
		Description: "d", Phase: "unassigned"}
	if err := h.DB.Create(&ticket).Error; err != nil {
		t.Fatalf("seed ticket: %v", err)
	}
	return h, mux, cookie, ticket
}

func reload(t *testing.T, h *api.Handlers, id string) db.Ticket {
	t.Helper()
	var got db.Ticket
	if err := h.DB.First(&got, "id = ?", id).Error; err != nil {
		t.Fatalf("reload ticket: %v", err)
	}
	return got
}

func TestSetModelsOnAnUnclaimedTicket(t *testing.T) {
	h, mux, cookie, ticket := setModelsFixture(t)

	w := postModelAction(t, mux, cookie, ticket.ID, url.Values{
		"action": {"set-models"}, "model_backend": {"claude-code"}, "model_brainstorm": {"opus"},
	})
	if w.Code != http.StatusNoContent {
		t.Fatalf("status = %d: %s", w.Code, w.Body.String())
	}
	got := reload(t, h, ticket.ID)
	if got.ModelSelections()["brainstorm"] != "opus" {
		t.Errorf("selections = %v, want brainstorm=opus", got.ModelSelections())
	}
	if got.ModelBackend != "claude-code" {
		t.Errorf("model_backend = %q, want claude-code", got.ModelBackend)
	}

	var entries []db.LogEntry
	h.DB.Where("ticket_id = ? AND entry_type = ?", ticket.ID, "STATUS").Find(&entries)
	if len(entries) != 1 || !strings.Contains(entries[0].Message, "brainstorm=opus") {
		t.Errorf("STATUS entries = %+v, want one naming the selection", entries)
	}
}

func TestSetModelsIsRefusedOnAClaimedOrClosedTicket(t *testing.T) {
	tests := []struct {
		name    string
		updates map[string]any
	}{
		{"claimed", map[string]any{"assigned_shem": uint(1)}},
		{"closed", map[string]any{"phase": "closed"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, mux, cookie, ticket := setModelsFixture(t)
			h.DB.Model(&ticket).Updates(tt.updates)

			w := postModelAction(t, mux, cookie, ticket.ID, url.Values{
				"action": {"set-models"}, "model_backend": {"claude-code"}, "model_brainstorm": {"opus"},
			})
			if w.Code != http.StatusConflict {
				t.Errorf("status = %d, want 409: %s", w.Code, w.Body.String())
			}
			if got := reload(t, h, ticket.ID); len(got.ModelSelections()) != 0 {
				t.Errorf("selections = %v, want none written", got.ModelSelections())
			}
		})
	}
}

func TestSetModelsRequiresCSRFAndPermission(t *testing.T) {
	t.Run("no CSRF token", func(t *testing.T) {
		h, mux, cookie, ticket := setModelsFixture(t)
		req := httptest.NewRequest(http.MethodPost, "/api/tickets/"+ticket.ID+"/actions",
			strings.NewReader("action=set-models&model_backend=claude-code&model_brainstorm=opus"))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.AddCookie(cookie)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		if w.Code != http.StatusForbidden {
			t.Errorf("status = %d, want 403: %s", w.Code, w.Body.String())
		}
		if got := reload(t, h, ticket.ID); len(got.ModelSelections()) != 0 {
			t.Errorf("selections = %v, want none written", got.ModelSelections())
		}
	})

	t.Run("without PermTicketManage", func(t *testing.T) {
		h, mux, _, ticket := setModelsFixture(t)
		// rolePermissions denies by default, so a role absent from it has no
		// permissions at all.
		_, viewerCookie := seedSessionUser(t, h.DB, "viewer", "no-such-role")
		w := postModelAction(t, mux, viewerCookie, ticket.ID, url.Values{
			"action": {"set-models"}, "model_backend": {"claude-code"}, "model_brainstorm": {"opus"},
		})
		if w.Code == http.StatusNoContent {
			t.Error("a viewer set a ticket's models")
		}
		if got := reload(t, h, ticket.ID); len(got.ModelSelections()) != 0 {
			t.Errorf("selections = %v, want none written", got.ModelSelections())
		}
	})
}

func TestSetModelsWithAnEmptySubmissionClears(t *testing.T) {
	h, mux, cookie, ticket := setModelsFixture(t)
	h.DB.Model(&ticket).Updates(map[string]any{
		"models": `{"brainstorm":"opus"}`, "model_backend": "claude-code"})

	w := postModelAction(t, mux, cookie, ticket.ID, url.Values{"action": {"set-models"}})
	if w.Code != http.StatusNoContent {
		t.Fatalf("status = %d: %s", w.Code, w.Body.String())
	}
	got := reload(t, h, ticket.ID)
	if got.Models != "{}" {
		t.Errorf("models = %q, want {}", got.Models)
	}
	if got.ModelBackend != "" {
		t.Errorf("model_backend = %q, want empty", got.ModelBackend)
	}
}

func TestSetModelsLeavesTheApprovalHashesAlone(t *testing.T) {
	h, mux, cookie, ticket := setModelsFixture(t)
	hash := ghsync.HashDescription("d")
	n := 7
	h.DB.Model(&ticket).Updates(map[string]any{
		"body_hash": hash, "approved_body_hash": hash,
		"intake_approved": true, "issue_number": &n})

	w := postModelAction(t, mux, cookie, ticket.ID, url.Values{
		"action": {"set-models"}, "model_backend": {"claude-code"}, "model_brainstorm": {"opus"},
	})
	if w.Code != http.StatusNoContent {
		t.Fatalf("status = %d: %s", w.Code, w.Body.String())
	}
	got := reload(t, h, ticket.ID)
	if got.BodyHash != hash || got.ApprovedBodyHash != hash {
		t.Errorf("hashes changed: body=%q approved=%q", got.BodyHash, got.ApprovedBodyHash)
	}
	if got.Phase != "unassigned" || !got.IntakeApproved {
		t.Errorf("phase = %q, intake_approved = %v; want unassigned and true", got.Phase, got.IntakeApproved)
	}
}

func TestSetModelsRejectsAnInvalidSelection(t *testing.T) {
	tests := []struct {
		name string
		form url.Values
	}{
		{"unknown model", url.Values{"model_backend": {"claude-code"}, "model_brainstorm": {"gpt-big"}}},
		{"unreported backend", url.Values{"model_backend": {"gemini"}, "model_brainstorm": {"opus"}}},
		{"no selection support", url.Values{"model_backend": {"single"}, "model_brainstorm": {"only"}}},
		{"a model with no backend", url.Values{"model_brainstorm": {"opus"}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, mux, cookie, ticket := setModelsFixture(t)
			form := tt.form
			form.Set("action", "set-models")
			w := postModelAction(t, mux, cookie, ticket.ID, form)
			if w.Code != http.StatusBadRequest {
				t.Errorf("status = %d, want 400: %s", w.Code, w.Body.String())
			}
			if got := reload(t, h, ticket.ID); len(got.ModelSelections()) != 0 {
				t.Errorf("selections = %v, want none written", got.ModelSelections())
			}
		})
	}
}

func TestStartWritesModelSelections(t *testing.T) {
	startFixture := func(t *testing.T) (*api.Handlers, *http.ServeMux, *http.Cookie, db.Ticket) {
		t.Helper()
		h, mux, cookie := setupActionTest(t)
		seedShemWithCatalog(t, h.DB, "node-a", "key1", "claude-code", modelCatalog("opus"))
		n := 11
		hash := ghsync.HashDescription("d")
		ticket := db.Ticket{RepoRemote: "r", BaseBranch: "main", Branch: "b", Title: "t",
			Description: "d", Phase: "pending-approval", IssueNumber: &n, BodyHash: hash}
		if err := h.DB.Create(&ticket).Error; err != nil {
			t.Fatalf("seed ticket: %v", err)
		}
		return h, mux, cookie, ticket
	}

	t.Run("a successful start persists them", func(t *testing.T) {
		h, mux, cookie, ticket := startFixture(t)
		w := postModelAction(t, mux, cookie, ticket.ID, url.Values{
			"action":             {"start"},
			"reviewed_body_hash": {ghsync.HashDescription("d")},
			"model_backend":      {"claude-code"},
			"model_brainstorm":   {"opus"},
		})
		if w.Code != http.StatusNoContent {
			t.Fatalf("status = %d: %s", w.Code, w.Body.String())
		}
		got := reload(t, h, ticket.ID)
		if got.ModelSelections()["brainstorm"] != "opus" || got.ModelBackend != "claude-code" {
			t.Errorf("selections = %v, backend = %q", got.ModelSelections(), got.ModelBackend)
		}
	})

	t.Run("a stale reviewed hash writes nothing", func(t *testing.T) {
		h, mux, cookie, ticket := startFixture(t)
		w := postModelAction(t, mux, cookie, ticket.ID, url.Values{
			"action":             {"start"},
			"reviewed_body_hash": {ghsync.HashDescription("something else")},
			"model_backend":      {"claude-code"},
			"model_brainstorm":   {"opus"},
		})
		if w.Code == http.StatusNoContent {
			t.Fatal("a stale reviewed hash was accepted")
		}
		got := reload(t, h, ticket.ID)
		if len(got.ModelSelections()) != 0 || got.ModelBackend != "" {
			t.Errorf("a refused start wrote selections: %v / %q", got.ModelSelections(), got.ModelBackend)
		}
		if got.Phase != "pending-approval" {
			t.Errorf("phase = %q, want pending-approval", got.Phase)
		}
	})

	t.Run("an invalid model leaves the ticket unapproved", func(t *testing.T) {
		h, mux, cookie, ticket := startFixture(t)
		w := postModelAction(t, mux, cookie, ticket.ID, url.Values{
			"action":             {"start"},
			"reviewed_body_hash": {ghsync.HashDescription("d")},
			"model_backend":      {"claude-code"},
			"model_brainstorm":   {"gpt-big"},
		})
		if w.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400: %s", w.Code, w.Body.String())
		}
		got := reload(t, h, ticket.ID)
		if got.IntakeApproved || got.Phase != "pending-approval" {
			t.Errorf("phase = %q, intake_approved = %v", got.Phase, got.IntakeApproved)
		}
	})
}

func TestRequeueReplacesTheStoredSelections(t *testing.T) {
	requeueFixture := func(t *testing.T) (*api.Handlers, *http.ServeMux, *http.Cookie, db.Ticket) {
		t.Helper()
		h, mux, cookie := setupActionTest(t)
		seedShemWithCatalog(t, h.DB, "node-a", "key1", "claude-code", modelCatalog("opus", "haiku"))
		shemID := uint(1)
		ticket := db.Ticket{RepoRemote: "r", BaseBranch: "main", Branch: "b", Title: "t",
			Description: "d", Phase: "implement", AssignedShem: &shemID,
			Models: `{"brainstorm":"haiku"}`, ModelBackend: "claude-code"}
		if err := h.DB.Create(&ticket).Error; err != nil {
			t.Fatalf("seed ticket: %v", err)
		}
		return h, mux, cookie, ticket
	}

	t.Run("a submitted default replaces the stored set", func(t *testing.T) {
		h, mux, cookie, ticket := requeueFixture(t)
		w := postModelAction(t, mux, cookie, ticket.ID, url.Values{
			"action": {"requeue"}, "model_backend": {"claude-code"}, "model_default": {"opus"},
		})
		if w.Code != http.StatusNoContent {
			t.Fatalf("status = %d: %s", w.Code, w.Body.String())
		}
		got := reload(t, h, ticket.ID)
		sel := got.ModelSelections()
		if sel[models.DefaultKey] != "opus" {
			t.Errorf("default = %q, want opus", sel[models.DefaultKey])
		}
		if _, still := sel["brainstorm"]; still {
			t.Errorf("the stored brainstorm override survived: %v", sel)
		}
	})

	t.Run("no models leaves them", func(t *testing.T) {
		h, mux, cookie, ticket := requeueFixture(t)
		w := postModelAction(t, mux, cookie, ticket.ID, url.Values{"action": {"requeue"}})
		if w.Code != http.StatusNoContent {
			t.Fatalf("status = %d: %s", w.Code, w.Body.String())
		}
		if got := reload(t, h, ticket.ID); got.ModelSelections()["brainstorm"] != "haiku" {
			t.Errorf("selections = %v, want the stored set", got.ModelSelections())
		}
	})

	t.Run("an invalid value does not requeue", func(t *testing.T) {
		h, mux, cookie, ticket := requeueFixture(t)
		w := postModelAction(t, mux, cookie, ticket.ID, url.Values{
			"action": {"requeue"}, "model_backend": {"claude-code"}, "model_default": {"gpt-big"},
		})
		if w.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400: %s", w.Code, w.Body.String())
		}
		if got := reload(t, h, ticket.ID); got.Phase != "implement" {
			t.Errorf("phase = %q, want implement", got.Phase)
		}
	})
}

func TestRequestChangesFromReviewMergesTheReviseStage(t *testing.T) {
	fixture := func(t *testing.T, phase string) (*api.Handlers, *http.ServeMux, *http.Cookie, db.Ticket) {
		t.Helper()
		h, mux, cookie := setupActionTest(t)
		seedShemWithCatalog(t, h.DB, "node-a", "key1", "claude-code", modelCatalog("opus", "haiku"))
		shemID := uint(1)
		ticket := db.Ticket{RepoRemote: "r", BaseBranch: "main", Branch: "b", Title: "t",
			Description: "d", Phase: phase, AssignedShem: &shemID,
			Models: `{"brainstorm":"haiku"}`, ModelBackend: "claude-code"}
		if err := h.DB.Create(&ticket).Error; err != nil {
			t.Fatalf("seed ticket: %v", err)
		}
		return h, mux, cookie, ticket
	}

	t.Run("merges over the stored selections", func(t *testing.T) {
		h, mux, cookie, ticket := fixture(t, "ready-for-review")
		w := postModelAction(t, mux, cookie, ticket.ID, url.Values{
			"action": {"request-changes"}, "feedback": {"please fix"},
			"model_backend": {"claude-code"}, "model_revise": {"opus"},
		})
		if w.Code != http.StatusNoContent {
			t.Fatalf("status = %d: %s", w.Code, w.Body.String())
		}
		sel := reload(t, h, ticket.ID).ModelSelections()
		if sel["revise"] != "opus" {
			t.Errorf("revise = %q, want opus", sel["revise"])
		}
		if sel["brainstorm"] != "haiku" {
			t.Errorf("brainstorm = %q, want the stored haiku", sel["brainstorm"])
		}
	})

	t.Run("the phase guard writes nothing", func(t *testing.T) {
		h, mux, cookie, ticket := fixture(t, "ready-for-review")
		// Move it out from under the transaction's WHERE clause.
		h.DB.Model(&ticket).Update("phase", "implement")
		w := postModelAction(t, mux, cookie, ticket.ID, url.Values{
			"action": {"request-changes"}, "feedback": {"please fix"},
			"model_backend": {"claude-code"}, "model_revise": {"opus"},
		})
		if w.Code == http.StatusNoContent {
			t.Fatal("request-changes succeeded outside ready-for-review")
		}
		if sel := reload(t, h, ticket.ID).ModelSelections(); sel["revise"] != "" {
			t.Errorf("a refused request-changes wrote revise=%q", sel["revise"])
		}
	})

	t.Run("the pending-approval path writes nothing", func(t *testing.T) {
		h, mux, cookie, ticket := fixture(t, "plan")
		hi := db.HumanInput{TicketID: ticket.ID, Kind: "approval", Prompt: "approve?"}
		if err := h.DB.Create(&hi).Error; err != nil {
			t.Fatal(err)
		}
		postModelAction(t, mux, cookie, ticket.ID, url.Values{
			"action": {"request-changes"}, "feedback": {"please fix"},
			"model_backend": {"claude-code"}, "model_revise": {"opus"},
		})
		got := reload(t, h, ticket.ID)
		if sel := got.ModelSelections(); sel["revise"] != "" {
			t.Errorf("the pending-approval path wrote revise=%q", sel["revise"])
		}
		if got.ModelBackend != "claude-code" {
			t.Errorf("model_backend = %q, want the stored claude-code", got.ModelBackend)
		}
	})
}

func TestATierOnlySubmissionBindsNoBackend(t *testing.T) {
	t.Run("set-models", func(t *testing.T) {
		h, mux, cookie, ticket := setModelsFixture(t)
		w := postModelAction(t, mux, cookie, ticket.ID, url.Values{
			"action": {"set-models"}, "model_backend": {"claude-code"}, "model_brainstorm": {"tier:large"},
		})
		if w.Code != http.StatusNoContent {
			t.Fatalf("status = %d: %s", w.Code, w.Body.String())
		}
		got := reload(t, h, ticket.ID)
		if got.ModelBackend != "" {
			t.Errorf("model_backend = %q, want empty", got.ModelBackend)
		}
		if got.ModelSelections()["brainstorm"] != "tier:large" {
			t.Errorf("selections = %v", got.ModelSelections())
		}
	})

	t.Run("requeue", func(t *testing.T) {
		h, mux, cookie := setupActionTest(t)
		seedShemWithCatalog(t, h.DB, "node-a", "key1", "claude-code", modelCatalog("opus"))
		shemID := uint(1)
		ticket := db.Ticket{RepoRemote: "r", BaseBranch: "main", Branch: "b", Title: "t",
			Description: "d", Phase: "implement", AssignedShem: &shemID, ModelBackend: "claude-code"}
		if err := h.DB.Create(&ticket).Error; err != nil {
			t.Fatal(err)
		}
		w := postModelAction(t, mux, cookie, ticket.ID, url.Values{
			"action": {"requeue"}, "model_backend": {"claude-code"}, "model_default": {"tier:large"},
		})
		if w.Code != http.StatusNoContent {
			t.Fatalf("status = %d: %s", w.Code, w.Body.String())
		}
		if got := reload(t, h, ticket.ID); got.ModelBackend != "" {
			t.Errorf("model_backend = %q, want empty", got.ModelBackend)
		}
	})

	t.Run("start", func(t *testing.T) {
		h, mux, cookie := setupActionTest(t)
		seedShemWithCatalog(t, h.DB, "node-a", "key1", "claude-code", modelCatalog("opus"))
		n := 12
		hash := ghsync.HashDescription("d")
		ticket := db.Ticket{RepoRemote: "r", BaseBranch: "main", Branch: "b", Title: "t",
			Description: "d", Phase: "pending-approval", IssueNumber: &n, BodyHash: hash}
		if err := h.DB.Create(&ticket).Error; err != nil {
			t.Fatal(err)
		}
		w := postModelAction(t, mux, cookie, ticket.ID, url.Values{
			"action": {"start"}, "reviewed_body_hash": {hash},
			"model_backend": {"claude-code"}, "model_default": {"tier:large"},
		})
		if w.Code != http.StatusNoContent {
			t.Fatalf("status = %d: %s", w.Code, w.Body.String())
		}
		if got := reload(t, h, ticket.ID); got.ModelBackend != "" {
			t.Errorf("model_backend = %q, want empty", got.ModelBackend)
		}
	})
}

// A revision runs on the assigned shem, so its model is checked against that
// shem's own catalog, not the fleet's.
func TestRequestChangesChecksTheAssignedShemsCatalog(t *testing.T) {
	h, mux, cookie := setupActionTest(t)
	seedShemWithCatalog(t, h.DB, "node-a", "key1", "claude-code", modelCatalog("haiku"))
	seedShemWithCatalog(t, h.DB, "node-b", "key2", "claude-code", modelCatalog("haiku", "opus"))
	shemID := uint(1)
	ticket := db.Ticket{RepoRemote: "r", BaseBranch: "main", Branch: "b", Title: "t",
		Description: "d", Phase: "ready-for-review", AssignedShem: &shemID}
	if err := h.DB.Create(&ticket).Error; err != nil {
		t.Fatalf("seed ticket: %v", err)
	}
	for _, tt := range []struct {
		name    string
		backend string
		model   string
		want    int
	}{
		{"a model only another shem has", "claude-code", "opus", http.StatusBadRequest},
		{"a backend the assigned shem does not run", "codex", "haiku", http.StatusBadRequest},
		{"a model the assigned shem has", "claude-code", "haiku", http.StatusNoContent},
	} {
		t.Run(tt.name, func(t *testing.T) {
			w := postModelAction(t, mux, cookie, ticket.ID, url.Values{
				"action": {"request-changes"}, "feedback": {"please fix"},
				"model_backend": {tt.backend}, "model_revise": {tt.model},
			})
			if w.Code != tt.want {
				t.Errorf("status = %d, want %d: %s", w.Code, tt.want, w.Body.String())
			}
		})
	}
}

// A stored size the assigned shem lacks does not block choosing a revision
// model it has.
func TestRequestChangesKeepsAStoredSizeTheShemLacks(t *testing.T) {
	h, mux, cookie := setupActionTest(t)
	seedShemWithCatalog(t, h.DB, "node-a", "key1", "claude-code", modelCatalog("haiku"))
	shemID := uint(1)
	ticket := db.Ticket{RepoRemote: "r", BaseBranch: "main", Branch: "b", Title: "t",
		Description: "d", Phase: "ready-for-review", AssignedShem: &shemID, Models: `{"default":"tier:huge"}`}
	if err := h.DB.Create(&ticket).Error; err != nil {
		t.Fatalf("seed ticket: %v", err)
	}
	w := postModelAction(t, mux, cookie, ticket.ID, url.Values{
		"action": {"request-changes"}, "feedback": {"please fix"},
		"model_backend": {"claude-code"}, "model_revise": {"haiku"},
	})
	if w.Code != http.StatusNoContent {
		t.Fatalf("status = %d: %s", w.Code, w.Body.String())
	}
	sel := reload(t, h, ticket.ID).ModelSelections()
	if sel["revise"] != "haiku" || sel["default"] != "tier:huge" {
		t.Errorf("selections = %v, want revise=haiku over the kept default", sel)
	}
}
