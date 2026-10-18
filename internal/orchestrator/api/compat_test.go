package api_test

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"

	"github.com/leonp92/golem/internal/orchestrator/api"
	"github.com/leonp92/golem/internal/orchestrator/db"
	"github.com/leonp92/golem/internal/orchestrator/sse"
	ws "github.com/leonp92/golem/internal/orchestrator/ws"
)

// A database written by the pre-epic orchestrator (testdata/pre_epic.sql,
// dumped from origin/main's db.Open) migrates forward, and its tickets
// list, claim, resume and revise exactly as before.
func TestPreEpicDatabaseKeepsWorking(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	dump, err := os.ReadFile("testdata/pre_epic.sql")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := gorm.Open(sqlite.Open(path), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, _ := raw.DB()
	t.Cleanup(func() { _ = sqlDB.Close() })
	for _, stmt := range strings.Split(string(dump), ";\n") {
		if s := strings.TrimSpace(stmt); s != "" {
			if err := raw.Exec(s).Error; err != nil {
				t.Fatalf("load fixture: %v\n%s", err, s)
			}
		}
	}
	_ = sqlDB.Close()

	gdb, err := db.Open(path)
	if err != nil {
		t.Fatalf("migrate pre-epic database: %v", err)
	}
	t.Cleanup(func() { s, _ := gdb.DB(); _ = s.Close() })
	h := api.NewHandlers(gdb, ws.NewHub(), sse.NewBroker())
	mux := http.NewServeMux()
	h.RegisterTicketRoutes(mux)
	call := func(method, path string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, shemRequest(method, path, "old-shem", "oldkey"))
		return w
	}

	if w := call(http.MethodGet, "/api/tickets/available"); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "old-unassigned") {
		t.Fatalf("available: %d %s", w.Code, w.Body.String())
	}
	if w := call(http.MethodGet, "/api/tickets/resumable"); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "old-assigned") {
		t.Fatalf("resumable: %d %s", w.Code, w.Body.String())
	}
	w := call(http.MethodPost, "/api/tickets/old-unassigned/claim")
	if w.Code != http.StatusOK || strings.Contains(w.Body.String(), `"epic"`) {
		t.Fatalf("claim: %d %s", w.Code, w.Body.String())
	}
	// The tester gate is opt-in: a ticket written before the flag existed
	// migrates forward with it off and claims exactly as before.
	if !strings.Contains(w.Body.String(), `"tester_required":false`) {
		t.Fatalf("legacy ticket gained the tester gate: %s", w.Body.String())
	}
	// Self-verification became the default for every ticket: a ticket
	// written before the opt-out existed migrates forward with it on.
	if !strings.Contains(w.Body.String(), `"verify_disabled":false`) {
		t.Fatalf("legacy ticket lost self-verification: %s", w.Body.String())
	}
	if w := call(http.MethodPost, "/api/tickets/old-revising/revise-claim"); w.Code != http.StatusOK {
		t.Fatalf("revise-claim: %d %s", w.Code, w.Body.String())
	}
	var tk db.Ticket
	if err := gdb.First(&tk, "id = 'old-issue'").Error; err != nil || tk.EpicID != nil || tk.PRNumber == nil || *tk.PRNumber != 42 {
		t.Fatalf("issue ticket after migration: %+v %v", tk, err)
	}
}
