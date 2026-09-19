package api_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"golang.org/x/crypto/bcrypt"

	"github.com/leonp92/golem/internal/orchestrator/api"
	"github.com/leonp92/golem/internal/orchestrator/db"
	"github.com/leonp92/golem/internal/orchestrator/sse"
	ws "github.com/leonp92/golem/internal/orchestrator/ws"
)

// setupOwnershipTest registers the log and human-input routes on one mux and
// returns handlers plus two registered shems, so a test can drive a request
// from a shem the ticket is NOT assigned to.
func setupOwnershipTest(t *testing.T) (*api.Handlers, *http.ServeMux, db.Shem, db.Shem) {
	t.Helper()
	gdb, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	h := api.NewHandlers(gdb, ws.NewHub(), sse.NewBroker())
	mux := http.NewServeMux()
	h.RegisterLogRoutes(mux)
	h.RegisterHumanRoutes(mux)

	seed := func(name, key string) db.Shem {
		hash, _ := bcrypt.GenerateFromPassword([]byte(key), bcrypt.MinCost)
		s := db.Shem{Name: name, APIKeyHash: string(hash), Repos: "[]", Status: "online"}
		if err := gdb.Create(&s).Error; err != nil {
			t.Fatalf("seed shem %s: %v", name, err)
		}
		return s
	}
	return h, mux, seed("shemA", "keyA"), seed("shemB", "keyB")
}

// assignTicketToShem makes ticketID owned by the named shem. The shem-facing
// log and human-input endpoints are scoped to the calling shem's own ticket
// (finding S8), and the real flow always claims a ticket before writing to
// it, so a test that drives those endpoints has to claim it too.
func assignTicketToShem(t *testing.T, h *api.Handlers, ticketID, shemName string) {
	t.Helper()
	var shem db.Shem
	if err := h.DB.Where("name = ?", shemName).First(&shem).Error; err != nil {
		t.Fatalf("lookup shem %s: %v", shemName, err)
	}
	if err := h.DB.Model(&db.Ticket{}).Where("id = ?", ticketID).
		Update("assigned_shem", shem.ID).Error; err != nil {
		t.Fatalf("assign ticket to %s: %v", shemName, err)
	}
}

// TestShemWritesAreScopedToTheOwningTicket covers finding S8. postLog,
// postLogDocument, createHumanInput, listHumanInputs and resolveHumanInput
// enforced authentication but not ownership, so any registered shem could
// write into any ticket's log — which is the dashboard's md-content sink —
// and overwrite any ticket's SPEC or PLAN. That is the delivery route for a
// stored-markup payload; round 1a closed the payload at the sanitizer, this
// closes the route.
//
// Verified against HEAD ccff9ab before the fix: shemB got 200 from log,
// 200 from log/document, 201 from human-inputs, 200 from the list, and 204
// from resolve, all on shemA's ticket.
func TestShemWritesAreScopedToTheOwningTicket(t *testing.T) {
	type request struct {
		method  string
		path    string
		body    string
		headers map[string]string
	}
	cases := []struct {
		name      string
		build     func(ticketID string, inputID uint) request
		wantOwner int
	}{
		{
			name: "postLog",
			build: func(id string, _ uint) request {
				return request{
					method: http.MethodPost, path: "/api/tickets/" + id + "/log",
					body:    `{"entry_type":"STATUS","from_role":"developer","message":"m"}`,
					headers: map[string]string{"Content-Type": "application/json"},
				}
			},
			wantOwner: http.StatusOK,
		},
		{
			name: "postLogDocument",
			build: func(id string, _ uint) request {
				return request{
					method: http.MethodPost, path: "/api/tickets/" + id + "/log/document",
					body:    "# spec",
					headers: map[string]string{"X-Entry-Type": "SPEC", "X-From-Role": "developer"},
				}
			},
			wantOwner: http.StatusOK,
		},
		{
			name: "createHumanInput",
			build: func(id string, _ uint) request {
				return request{
					method: http.MethodPost, path: "/api/tickets/" + id + "/human-inputs",
					body:    `{"kind":"approval","prompt":"p"}`,
					headers: map[string]string{"Content-Type": "application/json"},
				}
			},
			wantOwner: http.StatusCreated,
		},
		{
			name: "listHumanInputs",
			build: func(id string, _ uint) request {
				return request{method: http.MethodGet, path: "/api/tickets/" + id + "/human-inputs"}
			},
			wantOwner: http.StatusOK,
		},
		{
			name: "resolveHumanInput",
			build: func(id string, inputID uint) request {
				return request{
					method: http.MethodPatch,
					path:   fmt.Sprintf("/api/tickets/%s/human-inputs/%d", id, inputID),
					body:   `{"response":"r"}`, headers: map[string]string{"Content-Type": "application/json"},
				}
			},
			wantOwner: http.StatusNoContent,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, mux, owner, stranger := setupOwnershipTest(t)

			ticket := db.Ticket{
				RepoRemote: "https://github.com/org/repo", Title: "t", Branch: "b",
				Description: "d", Phase: "implement", AssignedShem: &owner.ID,
			}
			if err := h.DB.Create(&ticket).Error; err != nil {
				t.Fatalf("seed ticket: %v", err)
			}
			// feedback is the only kind a shem key may resolve; this test is
			// about WHICH shem may resolve it, not which kind.
			hi := db.HumanInput{TicketID: ticket.ID, Kind: "feedback", Prompt: "p"}
			if err := h.DB.Create(&hi).Error; err != nil {
				t.Fatalf("seed human input: %v", err)
			}

			do := func(shemName, key string) *httptest.ResponseRecorder {
				req := tc.build(ticket.ID, hi.ID)
				var rdr io.Reader
				if req.body != "" {
					rdr = bytes.NewReader([]byte(req.body))
				}
				r := httptest.NewRequest(req.method, req.path, rdr)
				for k, v := range req.headers {
					r.Header.Set(k, v)
				}
				r.Header.Set("X-Shem-Name", shemName)
				r.Header.Set("Authorization", "Bearer "+key)
				w := httptest.NewRecorder()
				mux.ServeHTTP(w, r)
				return w
			}

			// A shem the ticket is not assigned to must be refused.
			if got := do("shemB", "keyB").Code; got != http.StatusConflict {
				t.Errorf("stranger shem (id %d) status = %d, want 409", stranger.ID, got)
			}
			// The owning shem is unaffected.
			if got := do("shemA", "keyA").Code; got != tc.wantOwner {
				t.Errorf("owning shem status = %d, want %d", got, tc.wantOwner)
			}
		})
	}
}

// TestStrangerShemWritesNothing asserts the refusal in S8 is a refusal, not
// just a status code: nothing reaches the log or human-input tables.
func TestStrangerShemWritesNothing(t *testing.T) {
	h, mux, owner, _ := setupOwnershipTest(t)

	ticket := db.Ticket{
		RepoRemote: "https://github.com/org/repo", Title: "t", Branch: "b",
		Description: "d", Phase: "implement", AssignedShem: &owner.ID,
	}
	if err := h.DB.Create(&ticket).Error; err != nil {
		t.Fatalf("seed ticket: %v", err)
	}

	post := func(path, body string, headers map[string]string) {
		r := httptest.NewRequest(http.MethodPost, path, bytes.NewReader([]byte(body)))
		for k, v := range headers {
			r.Header.Set(k, v)
		}
		r.Header.Set("X-Shem-Name", "shemB")
		r.Header.Set("Authorization", "Bearer keyB")
		mux.ServeHTTP(httptest.NewRecorder(), r)
	}
	payload, _ := json.Marshal(map[string]string{
		"entry_type": "STATUS", "from_role": "developer",
		"message": `<img src=x onerror=alert(1)>`,
	})
	post("/api/tickets/"+ticket.ID+"/log", string(payload),
		map[string]string{"Content-Type": "application/json"})
	post("/api/tickets/"+ticket.ID+"/log/document", "# hostile spec",
		map[string]string{"X-Entry-Type": "SPEC", "X-From-Role": "developer"})
	post("/api/tickets/"+ticket.ID+"/human-inputs", `{"kind":"approval","prompt":"p"}`,
		map[string]string{"Content-Type": "application/json"})

	var logs, inputs int64
	h.DB.Model(&db.LogEntry{}).Where("ticket_id = ?", ticket.ID).Count(&logs)
	h.DB.Model(&db.HumanInput{}).Where("ticket_id = ?", ticket.ID).Count(&inputs)
	if logs != 0 {
		t.Errorf("log entries written by a stranger shem = %d, want 0", logs)
	}
	if inputs != 0 {
		t.Errorf("human inputs written by a stranger shem = %d, want 0", inputs)
	}
}
