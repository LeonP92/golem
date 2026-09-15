package api_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"runtime"
	"strconv"
	"testing"

	"github.com/leonp92/golem/internal/orchestrator/db"
)

// dispatcherActions parses ticketAction's switch statement directly out of
// human.go and returns every action string it handles, in source order
// (excluding "default"). This drives the exhaustiveness test below from the
// dispatcher's own cases rather than a hand-copied list, so a newly added
// action is picked up automatically and must be consciously classified
// instead of silently inheriting "safe" behaviour by omission — which is
// exactly how actionNeedsAttention's pending-approval hole (fix round 2)
// went unnoticed.
func dispatcherActions(t *testing.T) []string {
	t.Helper()

	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("dispatcherActions: runtime.Caller could not resolve this test file's path")
	}
	srcPath := filepath.Join(filepath.Dir(thisFile), "human.go")

	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, srcPath, nil, 0)
	if err != nil {
		t.Fatalf("dispatcherActions: parse %s: %v", srcPath, err)
	}

	var actions []string
	ast.Inspect(file, func(n ast.Node) bool {
		fn, isFunc := n.(*ast.FuncDecl)
		if !isFunc || fn.Name.Name != "ticketAction" {
			return true
		}
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			sw, isSwitch := n.(*ast.SwitchStmt)
			if !isSwitch {
				return true
			}
			for _, stmt := range sw.Body.List {
				clause, isCase := stmt.(*ast.CaseClause)
				if !isCase || clause.List == nil {
					// clause.List == nil is the "default" clause.
					continue
				}
				for _, expr := range clause.List {
					lit, isLit := expr.(*ast.BasicLit)
					if !isLit || lit.Kind != token.STRING {
						continue
					}
					action, err := strconv.Unquote(lit.Value)
					if err != nil {
						continue
					}
					actions = append(actions, action)
				}
			}
			return false // ticketAction has one switch; no need to recurse further.
		})
		return false // found ticketAction; stop descending elsewhere.
	})

	if len(actions) == 0 {
		t.Fatal("dispatcherActions: found no case clauses in ticketAction's switch " +
			"— human.go's shape changed and this parser needs updating")
	}
	return actions
}

// TestEveryDispatcherActionOnPendingApprovalTicket is the durable, structural
// version of fix rounds 1 and 2 of the intake approval gate (spec Amendment
// 1): every action the ticketAction dispatcher knows about (enumerated by
// dispatcherActions from the switch in human.go itself, not a hand-copied
// list — see its doc comment) is run against a pending-approval ticket, and
// the resulting DB phase is asserted. Every action must leave the phase
// unchanged, with exactly two deliberate exceptions in wantPhase below.
//
// This is the test that should have caught actionNeedsAttention's hole
// before a reviewer had to: a newly added dispatcher action defaults to
// "must not move a pending-approval ticket" and fails this test until
// someone adds it to wantPhase after consciously deciding it's safe to.
func TestEveryDispatcherActionOnPendingApprovalTicket(t *testing.T) {
	// wantPhase is the only exception list this test allows. Every action
	// NOT named here must leave a pending-approval ticket's phase
	// unchanged at "pending-approval". Add a new action here ONLY after
	// deliberately deciding it is allowed to release a pending-approval
	// ticket — do not add an entry just to make a new test pass.
	wantPhase := map[string]string{
		"start": "unassigned",
		"close": "closed",
	}

	for _, action := range dispatcherActions(t) {
		t.Run(action, func(t *testing.T) {
			h, mux, cookie := setupActionTest(t)

			ticket := db.Ticket{RepoRemote: "r", Branch: "b", Description: "d", Phase: "pending-approval"}
			if err := h.DB.Create(&ticket).Error; err != nil {
				t.Fatalf("seed ticket: %v", err)
			}

			body := map[string]any{"action": action}
			switch action {
			case "answer":
				// Give it a resolvable HumanInput so the request reaches
				// actionAnswer's own logic instead of short-circuiting on
				// the dispatcher's input_id/response validation — either
				// way the assertion below holds, since actionAnswer never
				// touches ticket.Phase, but this exercises the real path.
				hi := db.HumanInput{TicketID: ticket.ID, Kind: "question_answer", Prompt: "q"}
				if err := h.DB.Create(&hi).Error; err != nil {
					t.Fatalf("seed human input: %v", err)
				}
				body["input_id"] = hi.ID
				body["response"] = "an answer"
			case "request-changes":
				body["feedback"] = "please fix"
			}

			raw, err := json.Marshal(body)
			if err != nil {
				t.Fatalf("marshal body: %v", err)
			}
			url := fmt.Sprintf("/api/tickets/%s/actions", ticket.ID)
			req := httptest.NewRequest(http.MethodPost, url, bytes.NewReader(raw))
			req.Header.Set("Content-Type", "application/json")
			req.AddCookie(cookie)
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, req)

			want, isException := wantPhase[action]
			if !isException {
				want = "pending-approval"
			}

			var got db.Ticket
			if err := h.DB.First(&got, "id = ?", ticket.ID).Error; err != nil {
				t.Fatalf("reload ticket: %v", err)
			}
			if got.Phase != want {
				t.Errorf("action=%q moved a pending-approval ticket to %q, want %q "+
					"(response status %d: %s)", action, got.Phase, want, w.Code, w.Body.String())
			}
		})
	}
}
