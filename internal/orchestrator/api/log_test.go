package api_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/leonp92/golem/internal/orchestrator/api"
	"github.com/leonp92/golem/internal/orchestrator/db"
	"github.com/leonp92/golem/internal/orchestrator/sse"
	ws "github.com/leonp92/golem/internal/orchestrator/ws"
	"golang.org/x/crypto/bcrypt"
)

func TestPostLog_AssignsSequenceNum(t *testing.T) {
	gdb, _ := db.Open(":memory:")
	ticket := db.Ticket{RepoRemote: "r", Branch: "b", Description: "d", Phase: "implement"}
	gdb.Create(&ticket)
	hash, _ := bcrypt.GenerateFromPassword([]byte("k"), bcrypt.MinCost)
	shem := db.Shem{Name: "s", APIKeyHash: string(hash), Repos: "[]", Status: "online"}
	gdb.Create(&shem)

	broker := sse.NewBroker()
	h := api.NewHandlers(gdb, ws.NewHub(), broker)
	mux := http.NewServeMux()
	h.RegisterLogRoutes(mux)

	body, _ := json.Marshal(map[string]string{
		"entry_type": "STATUS", "message": "done", "from_role": "developer",
	})
	req := httptest.NewRequest("POST", fmt.Sprintf("/api/tickets/%s/log", ticket.ID), bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer k")
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var resp map[string]uint
	json.NewDecoder(w.Body).Decode(&resp) //nolint:errcheck
	if resp["sequence_num"] != 1 {
		t.Errorf("expected sequence_num=1, got %d", resp["sequence_num"])
	}

	// Second entry gets seq 2
	req2 := httptest.NewRequest("POST", fmt.Sprintf("/api/tickets/%s/log", ticket.ID), bytes.NewReader(body))
	req2.Header.Set("Authorization", "Bearer k")
	req2.Header.Set("Content-Type", "application/json")
	w2 := httptest.NewRecorder()
	mux.ServeHTTP(w2, req2)
	json.NewDecoder(w2.Body).Decode(&resp) //nolint:errcheck
	if resp["sequence_num"] != 2 {
		t.Errorf("expected sequence_num=2, got %d", resp["sequence_num"])
	}
}

func TestPostLog_CreatesHumanInput_WhenToRoleHuman(t *testing.T) {
	gdb, _ := db.Open(":memory:")
	ticket := db.Ticket{RepoRemote: "r", Branch: "b", Description: "d", Phase: "brainstorm"}
	gdb.Create(&ticket)
	hash, _ := bcrypt.GenerateFromPassword([]byte("k"), bcrypt.MinCost)
	shem := db.Shem{Name: "s", APIKeyHash: string(hash), Repos: "[]", Status: "online"}
	gdb.Create(&shem)

	h := api.NewHandlers(gdb, ws.NewHub(), sse.NewBroker())
	mux := http.NewServeMux()
	h.RegisterLogRoutes(mux)

	body, _ := json.Marshal(map[string]string{
		"entry_type": "QUESTION", "to_role": "human",
		"message": "Which approach?", "from_role": "spec-adherence",
	})
	req := httptest.NewRequest("POST", fmt.Sprintf("/api/tickets/%s/log", ticket.ID), bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer k")
	req.Header.Set("Content-Type", "application/json")
	mux.ServeHTTP(httptest.NewRecorder(), req)

	var inputs []db.HumanInput
	gdb.Where("ticket_id = ?", ticket.ID).Find(&inputs)
	if len(inputs) != 1 || inputs[0].Kind != "question_answer" {
		t.Errorf("expected 1 question_answer human_input, got %+v", inputs)
	}
}
