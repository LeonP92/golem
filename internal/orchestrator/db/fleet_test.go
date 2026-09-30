package db_test

import (
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"

	"gorm.io/gorm"

	"github.com/leonp92/golem/internal/models"
	"github.com/leonp92/golem/internal/orchestrator/db"
)

func fleetDB(t *testing.T) *gorm.DB {
	t.Helper()
	gdb, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return gdb
}

func catalogJSON(t *testing.T, ids ...string) string {
	t.Helper()
	cat := models.Catalog{SupportsSelection: true, Tiers: []models.Tier{"large"}}
	for _, id := range ids {
		cat.Models = append(cat.Models, models.Model{ID: id, Tier: "large"})
	}
	data, err := json.Marshal(cat)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func addShem(t *testing.T, gdb *gorm.DB, name, backend, catalog, status string) {
	t.Helper()
	s := db.Shem{Name: name, APIKeyHash: "x", Repos: "[]", Backend: backend, Catalog: catalog, Status: status}
	if err := gdb.Create(&s).Error; err != nil {
		t.Fatalf("create shem %s: %v", name, err)
	}
}

func TestATicketWrittenBeforeThisChangeReadsEmpty(t *testing.T) {
	gdb := fleetDB(t)
	ticket := db.Ticket{RepoRemote: "r", Branch: "b", Description: "d"}
	if err := gdb.Create(&ticket).Error; err != nil {
		t.Fatalf("create ticket: %v", err)
	}
	var got db.Ticket
	if err := gdb.First(&got, "id = ?", ticket.ID).Error; err != nil {
		t.Fatal(err)
	}
	if got.Models != "{}" {
		t.Errorf("models = %q, want {}", got.Models)
	}
	if got.ModelBackend != "" {
		t.Errorf("model_backend = %q, want empty", got.ModelBackend)
	}

	cat := models.Catalog{
		SupportsSelection: true,
		Tiers:             []models.Tier{"large"},
		Models:            []models.Model{{ID: "opus", Tier: "large"}},
		StageDefaults:     map[models.Stage]models.Tier{models.StagePlan: "large"},
	}
	resolved, dropped := models.Resolve(cat, got.ModelSelections())
	if resolved[models.StagePlan] != "opus" {
		t.Errorf("plan = %q, want the stage default opus", resolved[models.StagePlan])
	}
	if dropped != nil {
		t.Errorf("dropped = %v, want nothing", dropped)
	}
}

func TestShemRowsReadBackWithNoBackend(t *testing.T) {
	gdb := fleetDB(t)
	addShem(t, gdb, "legacy", "", "{}", "online")
	var got db.Shem
	if err := gdb.First(&got, "name = ?", "legacy").Error; err != nil {
		t.Fatal(err)
	}
	if got.Backend != "" || got.Catalog != "{}" {
		t.Errorf("backend = %q, catalog = %q, want empty and {}", got.Backend, got.Catalog)
	}
	if len(got.ModelCatalog().Models) != 0 {
		t.Errorf("ModelCatalog = %+v, want empty", got.ModelCatalog())
	}
}

func TestFleetBackendsAndCatalog(t *testing.T) {
	gdb := fleetDB(t)
	addShem(t, gdb, "shem-z", "claude-code", catalogJSON(t, "opus"), "offline")
	addShem(t, gdb, "shem-a", "claude-code", catalogJSON(t, "haiku"), "online")
	addShem(t, gdb, "shem-m", "codex", catalogJSON(t, "gpt-big"), "online")
	addShem(t, gdb, "broken", "claude-code", "{not json", "online")
	addShem(t, gdb, "no-backend", "", catalogJSON(t, "ignored"), "online")

	f, err := db.LoadFleet(gdb)
	if err != nil {
		t.Fatalf("LoadFleet: %v", err)
	}
	backends := f.Backends()
	if len(backends) != 2 {
		t.Fatalf("Backends = %+v, want two", backends)
	}
	if backends[0].Name != "claude-code" || backends[1].Name != "codex" {
		t.Errorf("Backends order = %q, %q, want claude-code then codex", backends[0].Name, backends[1].Name)
	}

	cat, ok := f.Catalog("claude-code")
	if !ok {
		t.Fatal("claude-code is not reported")
	}
	// name asc, so shem-a's haiku precedes shem-z's opus; the malformed row is
	// skipped rather than fatal.
	var ids []string
	for _, m := range cat.Models {
		ids = append(ids, m.ID)
	}
	if strings.Join(ids, ",") != "haiku,opus" {
		t.Errorf("union models = %v, want haiku then opus", ids)
	}

	if _, ok := f.Catalog("gemini"); ok {
		t.Error("an unreported backend was reported as present")
	}
	if len(f.Catalogs()) != 2 {
		t.Errorf("Catalogs = %v, want two entries", f.Catalogs())
	}
}

func TestLoadFleetReportsAQueryFailure(t *testing.T) {
	gdb := fleetDB(t)
	if err := gdb.Migrator().DropTable(&db.Shem{}); err != nil {
		t.Fatal(err)
	}
	f, err := db.LoadFleet(gdb)
	if err == nil {
		t.Fatal("a failed query was reported as an empty fleet")
	}
	if len(f.Backends()) != 0 {
		t.Errorf("Backends = %+v, want none", f.Backends())
	}
}

func TestFleetWaitingFor(t *testing.T) {
	gdb := fleetDB(t)
	addShem(t, gdb, "shem-a", "claude-code", catalogJSON(t, "haiku"), "online")
	f, err := db.LoadFleet(gdb)
	if err != nil {
		t.Fatalf("LoadFleet: %v", err)
	}
	assigned := uint(1)

	tests := []struct {
		name   string
		ticket db.Ticket
		want   string
	}{
		{"claimable", db.Ticket{ModelBackend: "claude-code", Models: `{"plan":"haiku"}`}, ""},
		{"no model backend", db.Ticket{}, ""},
		{"already assigned", db.Ticket{ModelBackend: "codex", AssignedShem: &assigned}, ""},
		{
			"tier-only against a catalog lacking the tier",
			db.Ticket{ModelBackend: "claude-code", Models: `{"plan":"tier:huge"}`}, "",
		},
		{
			"no shem runs the backend",
			db.Ticket{ModelBackend: "codex", Models: `{"plan":"gpt-big"}`},
			"waiting for a codex shem",
		},
		{
			"no catalog holds every selection",
			db.Ticket{ModelBackend: "claude-code", Models: `{"plan":"opus"}`},
			"waiting for a claude-code shem whose catalog has plan=opus",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := f.WaitingFor(tt.ticket); got != tt.want {
				t.Errorf("WaitingFor = %q, want %q", got, tt.want)
			}
		})
	}
}

// A page that shows a badge per row must not issue a query per row.
func TestFleetIsLoadedWithOneQuery(t *testing.T) {
	gdb := fleetDB(t)
	addShem(t, gdb, "shem-a", "claude-code", catalogJSON(t, "haiku"), "online")

	var queries atomic.Int64
	if err := gdb.Callback().Query().After("gorm:query").Register("fleet_test:count", func(*gorm.DB) {
		queries.Add(1)
	}); err != nil {
		t.Fatal(err)
	}

	f, err := db.LoadFleet(gdb)
	if err != nil {
		t.Fatalf("LoadFleet: %v", err)
	}
	afterLoad := queries.Load()
	if afterLoad != 1 {
		t.Errorf("LoadFleet issued %d queries, want 1", afterLoad)
	}

	for range 5 {
		f.WaitingFor(db.Ticket{ModelBackend: "claude-code", Models: `{"plan":"haiku"}`})
	}
	f.Backends()
	f.Catalogs()
	if got := queries.Load(); got != afterLoad {
		t.Errorf("%d queries after the load, want none", got-afterLoad)
	}
}

func TestModelSelectionsToleratesMalformedJSON(t *testing.T) {
	for _, raw := range []string{"", "{", "null"} {
		got := db.Ticket{Models: raw}.ModelSelections()
		if len(got) != 0 {
			t.Errorf("ModelSelections(%q) = %v, want empty", raw, got)
		}
	}
}

func TestSetModelSelectionsRoundTrips(t *testing.T) {
	var ticket db.Ticket
	ticket.SetModelSelections(models.Selections{"plan": "opus"})
	if got := ticket.ModelSelections()["plan"]; got != "opus" {
		t.Errorf("plan = %q, want opus", got)
	}
}
