package api_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"golang.org/x/crypto/bcrypt"

	"gorm.io/gorm"

	"github.com/leonp92/golem/internal/models"
	"github.com/leonp92/golem/internal/orchestrator/api"
	"github.com/leonp92/golem/internal/orchestrator/db"
	"github.com/leonp92/golem/internal/orchestrator/rbac"
	ws "github.com/leonp92/golem/internal/orchestrator/ws"
)

// modelCatalog is a small selectable catalog holding the given ids at tier
// large, with every stage defaulting to it.
func modelCatalog(ids ...string) models.Catalog {
	cat := models.Catalog{SupportsSelection: true, Tiers: []models.Tier{"large"},
		StageDefaults: map[models.Stage]models.Tier{}}
	for _, id := range ids {
		cat.Models = append(cat.Models, models.Model{ID: id, Tier: "large"})
	}
	for _, st := range models.Stages {
		cat.StageDefaults[st] = "large"
	}
	return cat
}

func seedShemWithCatalog(t *testing.T, gdb *gorm.DB, name, key, backend string, cat models.Catalog) db.Shem {
	t.Helper()
	hash, _ := bcrypt.GenerateFromPassword([]byte(key), bcrypt.MinCost)
	catalogJSON, err := json.Marshal(cat)
	if err != nil {
		t.Fatal(err)
	}
	s := db.Shem{Name: name, APIKeyHash: string(hash), Repos: "[]", Status: "online",
		Backend: backend, Catalog: string(catalogJSON)}
	if err := gdb.Create(&s).Error; err != nil {
		t.Fatalf("seed shem %s: %v", name, err)
	}
	return s
}

func seedTicketWithModels(t *testing.T, gdb *gorm.DB, sel models.Selections, backend string) db.Ticket {
	t.Helper()
	ticket := db.Ticket{RepoRemote: "https://github.com/org/repo", BaseBranch: "main",
		Branch: "b", Title: "t", Description: "d", Phase: "unassigned", ModelBackend: backend}
	ticket.SetModelSelections(sel)
	if err := gdb.Create(&ticket).Error; err != nil {
		t.Fatalf("seed ticket: %v", err)
	}
	return ticket
}

func modelsTestDB(t *testing.T) (*api.Handlers, *gorm.DB) {
	t.Helper()
	gdb, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	return api.NewHandlers(gdb, ws.NewHub(), nil), gdb
}

func TestRegisterReportsTheBackend(t *testing.T) {
	h, gdb := modelsTestDB(t)
	seedShemWithCatalog(t, gdb, "node-a", "key1", "", models.Catalog{})

	mux := http.NewServeMux()
	h.RegisterShemRoutes(mux)

	put := func(t *testing.T, body map[string]any) {
		t.Helper()
		raw, _ := json.Marshal(body)
		req := httptest.NewRequest(http.MethodPut, "/api/shems/me", bytes.NewReader(raw))
		req.Header.Set("Authorization", "Bearer key1")
		req.Header.Set("X-Shem-Name", "node-a")
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("status = %d: %s", w.Code, w.Body.String())
		}
	}
	reload := func(t *testing.T) db.Shem {
		t.Helper()
		var s db.Shem
		if err := gdb.First(&s, "name = ?", "node-a").Error; err != nil {
			t.Fatal(err)
		}
		return s
	}

	put(t, map[string]any{"name": "node-a", "repos": []string{},
		"backend": map[string]any{"name": "claude-code", "catalog": modelCatalog("opus")}})
	got := reload(t)
	if got.Backend != "claude-code" {
		t.Errorf("backend = %q, want claude-code", got.Backend)
	}
	if len(got.ModelCatalog().Models) != 1 {
		t.Errorf("catalog = %+v, want one model", got.ModelCatalog())
	}

	// An older shem sends no backend; it must not blank what a newer one wrote.
	put(t, map[string]any{"name": "node-a", "repos": []string{}})
	got = reload(t)
	if got.Backend != "claude-code" || len(got.ModelCatalog().Models) != 1 {
		t.Errorf("a backend-less registration blanked the columns: %q / %s", got.Backend, got.Catalog)
	}

	// Deregistering keeps them: the catalog describes a machine, not a session.
	req := httptest.NewRequest(http.MethodDelete, "/api/shems/me", nil)
	req.Header.Set("Authorization", "Bearer key1")
	req.Header.Set("X-Shem-Name", "node-a")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	got = reload(t)
	if got.Backend != "claude-code" || len(got.ModelCatalog().Models) != 1 {
		t.Errorf("deregistration blanked the columns: %q / %s", got.Backend, got.Catalog)
	}
}

func TestClaimPathsAlwaysCarryABackendAndEveryStage(t *testing.T) {
	tests := []struct {
		name  string
		phase string
		claim func(h *api.Handlers, ticketID string, shemID uint) (*api.ClaimResponse, error)
	}{
		{
			name: "fresh claim", phase: "unassigned",
			claim: func(h *api.Handlers, id string, sid uint) (*api.ClaimResponse, error) {
				return h.ClaimTicket(id, sid)
			},
		},
		{
			name: "revise claim", phase: "revising",
			claim: func(h *api.Handlers, id string, sid uint) (*api.ClaimResponse, error) {
				return h.ReviseClaim(id, sid)
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, gdb := modelsTestDB(t)
			shem := seedShemWithCatalog(t, gdb, "node-a", "key1", "claude-code", modelCatalog("opus", "haiku"))
			ticket := seedTicketWithModels(t, gdb, models.Selections{"brainstorm": "opus"}, "claude-code")
			gdb.Model(&ticket).Updates(map[string]any{"phase": tt.phase, "assigned_shem": shem.ID})

			resp, err := tt.claim(h, ticket.ID, shem.ID)
			if err != nil {
				t.Fatalf("claim: %v", err)
			}
			if resp.Backend != shem.Backend {
				t.Errorf("Backend = %q, want %q", resp.Backend, shem.Backend)
			}
			if len(resp.Models) != len(models.Stages) {
				t.Errorf("Models has %d entries, want %d", len(resp.Models), len(models.Stages))
			}
			if resp.Models["brainstorm"] != "opus" {
				t.Errorf("brainstorm = %q, want opus", resp.Models["brainstorm"])
			}
			// ReviseClaim's response carries the title too.
			if resp.Title != "t" {
				t.Errorf("Title = %q, want t", resp.Title)
			}
		})
	}
}

func TestResumableTicketsCarryResolvedModels(t *testing.T) {
	h, gdb := modelsTestDB(t)
	shem := seedShemWithCatalog(t, gdb, "node-a", "key1", "claude-code", modelCatalog("opus"))
	ticket := seedTicketWithModels(t, gdb, models.Selections{"brainstorm": "opus"}, "claude-code")
	gdb.Model(&ticket).Updates(map[string]any{"phase": "implement", "assigned_shem": shem.ID})

	mux := http.NewServeMux()
	h.RegisterTicketRoutes(mux)
	req := httptest.NewRequest(http.MethodGet, "/api/tickets/resumable", nil)
	req.Header.Set("Authorization", "Bearer key1")
	req.Header.Set("X-Shem-Name", "node-a")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", w.Code, w.Body.String())
	}
	var claims []api.ClaimResponse
	if err := json.Unmarshal(w.Body.Bytes(), &claims); err != nil {
		t.Fatal(err)
	}
	if len(claims) != 1 {
		t.Fatalf("got %d resumable tickets, want 1", len(claims))
	}
	if claims[0].Backend != "claude-code" {
		t.Errorf("Backend = %q, want claude-code", claims[0].Backend)
	}
	if len(claims[0].Models) != len(models.Stages) {
		t.Errorf("Models has %d entries, want %d", len(claims[0].Models), len(models.Stages))
	}
}

func TestTwoBackendsResolveTheSameTierToDifferentModels(t *testing.T) {
	h, gdb := modelsTestDB(t)
	claude := seedShemWithCatalog(t, gdb, "node-a", "key1", "claude-code", modelCatalog("opus"))
	codex := seedShemWithCatalog(t, gdb, "node-b", "key2", "codex", modelCatalog("gpt-big"))

	first := seedTicketWithModels(t, gdb, models.Selections{models.DefaultKey: "tier:large"}, "")
	second := seedTicketWithModels(t, gdb, models.Selections{models.DefaultKey: "tier:large"}, "")

	a, err := h.ClaimTicket(first.ID, claude.ID)
	if err != nil {
		t.Fatalf("claude claim: %v", err)
	}
	b, err := h.ClaimTicket(second.ID, codex.ID)
	if err != nil {
		t.Fatalf("codex claim: %v", err)
	}
	if a.Models["plan"] != "opus" {
		t.Errorf("claude resolved plan to %q, want opus", a.Models["plan"])
	}
	if b.Models["plan"] != "gpt-big" {
		t.Errorf("codex resolved plan to %q, want gpt-big", b.Models["plan"])
	}
}

func TestAModelBackendBindsTheTicketToThatBackend(t *testing.T) {
	tests := []struct {
		name         string
		modelBackend string
		wantClaim    bool
	}{
		{"another vendor", "codex", false},
		{"unbound", "", true},
		{"this vendor", "claude-code", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, gdb := modelsTestDB(t)
			shem := seedShemWithCatalog(t, gdb, "node-a", "key1", "claude-code", modelCatalog("opus"))
			ticket := seedTicketWithModels(t, gdb, models.Selections{}, tt.modelBackend)

			mux := http.NewServeMux()
			h.RegisterTicketRoutes(mux)
			req := httptest.NewRequest(http.MethodGet, "/api/tickets/available", nil)
			req.Header.Set("Authorization", "Bearer key1")
			req.Header.Set("X-Shem-Name", "node-a")
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, req)
			var listed []db.Ticket
			if err := json.Unmarshal(w.Body.Bytes(), &listed); err != nil {
				t.Fatal(err)
			}
			if got := len(listed) == 1; got != tt.wantClaim {
				t.Errorf("/available listed %d tickets, want claimable=%v", len(listed), tt.wantClaim)
			}
			_, err := h.ClaimTicket(ticket.ID, shem.ID)
			if (err == nil) != tt.wantClaim {
				t.Errorf("claim error = %v, want claimable=%v", err, tt.wantClaim)
			}
		})
	}
}

func TestAShemWhoseCatalogLacksASelectedModelCannotClaim(t *testing.T) {
	h, gdb := modelsTestDB(t)
	without := seedShemWithCatalog(t, gdb, "node-a", "key1", "claude-code", modelCatalog("haiku"))
	with := seedShemWithCatalog(t, gdb, "node-b", "key2", "claude-code", modelCatalog("haiku", "opus"))
	ticket := seedTicketWithModels(t, gdb, models.Selections{"brainstorm": "opus"}, "claude-code")

	mux := http.NewServeMux()
	h.RegisterTicketRoutes(mux)
	req := httptest.NewRequest(http.MethodGet, "/api/tickets/available", nil)
	req.Header.Set("Authorization", "Bearer key1")
	req.Header.Set("X-Shem-Name", "node-a")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	var listed []db.Ticket
	if err := json.Unmarshal(w.Body.Bytes(), &listed); err != nil {
		t.Fatal(err)
	}
	if len(listed) != 0 {
		t.Errorf("/available offered a ticket selecting a model this shem lacks: %+v", listed)
	}
	if _, err := h.ClaimTicket(ticket.ID, without.ID); err == nil {
		t.Error("a shem whose catalog lacks the selected model claimed the ticket")
	}
	if _, err := h.ClaimTicket(ticket.ID, with.ID); err != nil {
		t.Errorf("the shem whose catalog has the model could not claim: %v", err)
	}
}

func TestClaimWithNoShemRowFails(t *testing.T) {
	h, gdb := modelsTestDB(t)
	ticket := seedTicketWithModels(t, gdb, models.Selections{}, "")
	if _, err := h.ClaimTicket(ticket.ID, 999); err == nil {
		t.Error("a claim for a shem id with no row succeeded")
	}
	var after db.Ticket
	if err := gdb.First(&after, "id = ?", ticket.ID).Error; err != nil {
		t.Fatal(err)
	}
	if after.Phase != "unassigned" {
		t.Errorf("phase = %q, want unassigned", after.Phase)
	}
}

func TestAnOfflineShemsCatalogStillCounts(t *testing.T) {
	_, gdb := modelsTestDB(t)
	shem := seedShemWithCatalog(t, gdb, "node-a", "key1", "claude-code", modelCatalog("opus"))
	gdb.Model(&shem).Update("status", "offline")

	fleet, err := db.LoadFleet(gdb)
	if err != nil {
		t.Fatalf("LoadFleet: %v", err)
	}
	sel, bound, err := models.ValidateSelections(fleet.Catalogs(), "claude-code",
		map[string]string{"brainstorm": "opus"})
	if err != nil {
		t.Fatalf("an offline shem's catalog was ignored: %v", err)
	}
	if bound != "claude-code" || sel["brainstorm"] != "opus" {
		t.Errorf("sel = %v, bound = %q", sel, bound)
	}
}

func TestAResumedTicketBoundToAnotherBackendWarnsAndFallsBack(t *testing.T) {
	h, gdb := modelsTestDB(t)
	shem := seedShemWithCatalog(t, gdb, "node-a", "key1", "claude-code", modelCatalog("opus"))
	ticket := seedTicketWithModels(t, gdb, models.Selections{"brainstorm": "gpt-big"}, "codex")
	gdb.Model(&ticket).Updates(map[string]any{"phase": "implement", "assigned_shem": shem.ID})

	mux := http.NewServeMux()
	h.RegisterTicketRoutes(mux)
	req := httptest.NewRequest(http.MethodGet, "/api/tickets/resumable", nil)
	req.Header.Set("Authorization", "Bearer key1")
	req.Header.Set("X-Shem-Name", "node-a")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	var claims []api.ClaimResponse
	if err := json.Unmarshal(w.Body.Bytes(), &claims); err != nil {
		t.Fatal(err)
	}
	if len(claims) != 1 {
		t.Fatalf("got %d resumable tickets, want 1", len(claims))
	}
	// The ids belong to another vendor, so the stage defaults apply.
	if claims[0].Models["brainstorm"] != "opus" {
		t.Errorf("brainstorm = %q, want the stage default opus", claims[0].Models["brainstorm"])
	}

	// Repeated resumable calls add no further WARNING.
	for i := 0; i < 3; i++ {
		mux.ServeHTTP(httptest.NewRecorder(), req.Clone(req.Context()))
	}

	var warnings []db.LogEntry
	gdb.Where("ticket_id = ? AND entry_type = ?", ticket.ID, "WARNING").Find(&warnings)
	if len(warnings) != 1 {
		t.Fatalf("got %d WARNING entries after repeated polls, want 1: %+v", len(warnings), warnings)
	}
	for _, want := range []string{"codex", "claude-code"} {
		if !strings.Contains(warnings[0].Message, want) {
			t.Errorf("the warning %q does not name %q", warnings[0].Message, want)
		}
	}
}

func TestCreateTicketWithModelSelections(t *testing.T) {
	tests := []struct {
		name         string
		backend      string
		models       map[string]string
		wantCode     int
		wantBackend  string
		wantSelected string
	}{
		{
			name: "a valid id and a matching backend", backend: "claude-code",
			models:   map[string]string{"brainstorm": "opus"},
			wantCode: http.StatusCreated, wantBackend: "claude-code", wantSelected: "opus",
		},
		{
			name: "a tier binds no backend", models: map[string]string{"brainstorm": "tier:large"},
			wantCode: http.StatusCreated, wantBackend: "", wantSelected: "tier:large",
		},
		{
			name: "no selection", wantCode: http.StatusCreated,
		},
		{
			name: "an unknown model id", backend: "claude-code",
			models: map[string]string{"brainstorm": "gpt-big"}, wantCode: http.StatusBadRequest,
		},
		{
			name: "an unknown stage key", backend: "claude-code",
			models: map[string]string{"nonesuch": "opus"}, wantCode: http.StatusBadRequest,
		},
		{
			name: "an unreported backend", backend: "gemini",
			models: map[string]string{"brainstorm": "opus"}, wantCode: http.StatusBadRequest,
		},
		{
			name: "a backend that does not support selection", backend: "single",
			models: map[string]string{"brainstorm": "only"}, wantCode: http.StatusBadRequest,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, mux := setupTicketTest(t)
			_, cookie := seedSessionUser(t, h.DB, "leon", string(rbac.RoleDeveloper))
			seedShemWithCatalog(t, h.DB, "node-a", "key1", "claude-code", modelCatalog("opus", "haiku"))
			seedShemWithCatalog(t, h.DB, "node-s", "key2", "single",
				models.Catalog{Models: []models.Model{{ID: "only"}}})

			body, _ := json.Marshal(map[string]any{
				"repo_remote": "https://github.com/org/repo",
				"branch":      "main",
				"title":       "Model Test",
				"description": "d",
				"backend":     tt.backend,
				"models":      tt.models,
			})
			req := httptest.NewRequest(http.MethodPost, "/api/tickets", bytes.NewReader(body))
			withSession(req, cookie)
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, req)

			if w.Code != tt.wantCode {
				t.Fatalf("status = %d, want %d: %s", w.Code, tt.wantCode, w.Body.String())
			}
			var count int64
			h.DB.Model(&db.Ticket{}).Count(&count)
			if tt.wantCode != http.StatusCreated {
				if count != 0 {
					t.Errorf("a rejected create left %d tickets", count)
				}
				return
			}
			var created db.Ticket
			if err := h.DB.First(&created).Error; err != nil {
				t.Fatal(err)
			}
			if created.ModelBackend != tt.wantBackend {
				t.Errorf("model_backend = %q, want %q", created.ModelBackend, tt.wantBackend)
			}
			if got := created.ModelSelections()["brainstorm"]; got != tt.wantSelected {
				t.Errorf("brainstorm selection = %q, want %q", got, tt.wantSelected)
			}
		})
	}
}

// A tier-only ticket names no vendor, so any backend may claim it.
func TestATierOnlyTicketIsClaimableByEveryBackend(t *testing.T) {
	h, gdb := modelsTestDB(t)
	claude := seedShemWithCatalog(t, gdb, "node-a", "key1", "claude-code", modelCatalog("opus"))
	codex := seedShemWithCatalog(t, gdb, "node-b", "key2", "codex", modelCatalog("gpt-big"))

	for _, shem := range []db.Shem{claude, codex} {
		ticket := seedTicketWithModels(t, gdb, models.Selections{"brainstorm": "tier:large"}, "")
		resp, err := h.ClaimTicket(ticket.ID, shem.ID)
		if err != nil {
			t.Fatalf("%s could not claim a tier-only ticket: %v", shem.Name, err)
		}
		if resp.Backend != shem.Backend {
			t.Errorf("Backend = %q, want %q", resp.Backend, shem.Backend)
		}
	}
}

func TestListModels(t *testing.T) {
	h, gdb := modelsTestDB(t)
	seedShemWithCatalog(t, gdb, "node-a", "key1", "claude-code", modelCatalog("opus"))
	seedShemWithCatalog(t, gdb, "node-b", "key2", "codex", modelCatalog("gpt-big"))
	_, cookie := seedSessionUser(t, gdb, "leon", string(rbac.RoleDeveloper))

	mux := http.NewServeMux()
	h.RegisterModelRoutes(mux)

	req := httptest.NewRequest(http.MethodGet, "/api/models", nil)
	req.AddCookie(cookie)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", w.Code, w.Body.String())
	}
	var got struct {
		Stages   []models.Stage      `json:"stages"`
		Backends []db.BackendCatalog `json:"backends"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Stages) != len(models.Stages) {
		t.Errorf("got %d stages, want %d", len(got.Stages), len(models.Stages))
	}
	if len(got.Backends) != 2 {
		t.Fatalf("got %d backends, want 2: %+v", len(got.Backends), got.Backends)
	}
	if got.Backends[0].Name != "claude-code" || got.Backends[1].Name != "codex" {
		t.Errorf("backends = %q, %q", got.Backends[0].Name, got.Backends[1].Name)
	}
	if len(got.Backends[0].Catalog.Models) != 1 {
		t.Errorf("claude-code catalog = %+v, want one model", got.Backends[0].Catalog)
	}

	// Without a session it is refused.
	anon := httptest.NewRequest(http.MethodGet, "/api/models", nil)
	aw := httptest.NewRecorder()
	mux.ServeHTTP(aw, anon)
	if aw.Code == http.StatusOK {
		t.Error("GET /api/models answered an unauthenticated request")
	}
}

// A set-models landing between the catalog check and the claim update leaves
// the ticket unclaimed instead of handing this shem a model it lacks.
func TestClaimFailsWhenSelectionsChangeMidClaim(t *testing.T) {
	h, gdb := modelsTestDB(t)
	shem := seedShemWithCatalog(t, gdb, "node-a", "key1", "claude-code", modelCatalog("opus"))
	ticket := seedTicketWithModels(t, gdb, models.Selections{"brainstorm": "opus"}, "claude-code")

	fired := false
	const hook = "test:change_models_mid_claim"
	if err := gdb.Callback().Query().After("gorm:query").Register(hook, func(tx *gorm.DB) {
		if fired || tx.Statement.Table != "tickets" {
			return
		}
		fired = true
		tx.Session(&gorm.Session{NewDB: true, SkipHooks: true}).
			Exec("UPDATE tickets SET models = ? WHERE id = ?", `{"brainstorm":"gpt-big"}`, ticket.ID)
	}); err != nil {
		t.Fatal(err)
	}
	defer gdb.Callback().Query().Remove(hook) //nolint:errcheck

	if _, err := h.ClaimTicket(ticket.ID, shem.ID); err == nil {
		t.Fatal("the claim succeeded although the selections changed after the catalog check")
	}
	var got db.Ticket
	gdb.First(&got, "id = ?", ticket.ID)
	if got.Phase != "unassigned" || got.AssignedShem != nil {
		t.Errorf("phase=%s assigned=%v, want the ticket left unclaimed", got.Phase, got.AssignedShem)
	}
}
