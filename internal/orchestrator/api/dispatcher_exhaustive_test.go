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
	"github.com/leonp92/golem/internal/orchestrator/ghsync"
)

// dispatcherActions parses ticketAction's switch statement directly out of
// human.go and returns every action string it handles, in source order
// (excluding "default"). This drives the exhaustiveness test below from the
// dispatcher's own cases rather than a hand-copied list, so a newly added
// action is picked up automatically and must be consciously classified
// instead of silently inheriting "safe" behaviour by omission — which is
// exactly how actionNeedsAttention's pending-approval hole (fix round 2)
// went unnoticed.
//
// Hardenings, each found by a reviewer demonstrating it makes this extractor
// pass vacuously while a real hole is open:
//
//  1. (fix round 3) A switch is only accepted as THE dispatcher switch if
//     its tag is the selector body.Action. Without this check, the first
//     *ast.SwitchStmt found inside ticketAction is assumed to be the
//     dispatcher — so nesting the real switch one level inside an unrelated
//     outer switch (e.g. an authz check) would silently extract that outer
//     switch's case labels instead: bogus action names, an exhaustiveness
//     test with 0 real actions, all green.
//  2. (fix round 3) A case expression that isn't a plain string literal
//     (e.g. a named constant introduced by a partial refactor) now fails the
//     test loudly instead of being silently skipped. Skipping is exactly as
//     dangerous as the vacuous-switch case: a partial conversion — some
//     cases literals, some constants — would silently drop the converted
//     ones from the enumeration while still reporting a "passing", just
//     incomplete, run.
//  3. (fix round 4) Checking only sel.Sel.Name == "Action" is still not
//     enough: a reviewer planted an outer switch on a DIFFERENT selector
//     that also happens to end in .Action (authz.Action) and the extractor
//     collapsed to that switch's one case again. isDispatcherSwitch below
//     additionally requires the selector's base identifier to be exactly
//     "body" — ticketAction's own decoded-request variable — closing that
//     off. This is inherently name-coupled to human.go's local variable
//     name; if that variable is ever renamed, this constant must move with
//     it, and the "no switch on body.Action found" fatal below is what
//     forces that to be noticed rather than silently producing zero
//     actions.
//  4. (fix round 4) This no longer stops at the first matching switch: it
//     keeps scanning ticketAction's whole body and fails loudly if it finds
//     a SECOND switch on body.Action, rather than silently picking
//     whichever one was encountered first. Exactly one is assumed to exist;
//     an ambiguity is a sign this parser needs a human to resolve it, not
//     silent first-match behaviour.
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
	matches := 0
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
			if !isDispatcherSwitch(sw) {
				// Not the switch on body.Action — keep descending. This is
				// what lets a switch on body.Action nested inside some other
				// switch still be found, instead of stopping at whichever
				// switch is encountered first.
				return true
			}
			matches++
			if matches > 1 {
				t.Fatal("dispatcherActions: found more than one switch on " +
					"body.Action in ticketAction — this parser assumes exactly " +
					"one dispatcher switch; resolve the ambiguity in human.go " +
					"or teach dispatcherActions which one is the real one")
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
						t.Fatalf("dispatcherActions: a case expression in "+
							"ticketAction's switch on body.Action is %T, not a "+
							"string literal — teach this parser to resolve it "+
							"(e.g. named constants) or keep dispatcher cases as "+
							"plain string literals", expr)
					}
					action, err := strconv.Unquote(lit.Value)
					if err != nil {
						t.Fatalf("dispatcherActions: could not unquote case %q: %v", lit.Value, err)
					}
					actions = append(actions, action)
				}
			}
			// Already extracted this switch's own cases; no need to descend
			// into its case bodies looking for more (e.g. nested) switches —
			// but the outer walk still visits this switch's siblings, which
			// is how a second, ambiguous match would still be found above.
			return false
		})
		return false // found ticketAction; stop descending into its siblings (other funcs).
	})

	if matches == 0 {
		t.Fatal("dispatcherActions: no switch on body.Action found in ticketAction " +
			"— human.go's shape changed and this parser needs updating")
	}
	if len(actions) == 0 {
		t.Fatal("dispatcherActions: found the dispatcher switch but no case " +
			"clauses in it — human.go's shape changed and this parser needs updating")
	}
	return actions
}

// isDispatcherSwitch reports whether sw is specifically the switch on the
// local variable selector body.Action — ticketAction's decoded request
// struct — not merely any switch whose tag ends in a field named "Action".
func isDispatcherSwitch(sw *ast.SwitchStmt) bool {
	sel, isSel := sw.Tag.(*ast.SelectorExpr)
	if !isSel || sel.Sel == nil || sel.Sel.Name != "Action" {
		return false
	}
	ident, isIdent := sel.X.(*ast.Ident)
	return isIdent && ident.Name == "body"
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

	n := 1
	for _, action := range dispatcherActions(t) {
		t.Run(action, func(t *testing.T) {
			h, mux, cookie := setupActionTest(t)

			// IssueNumber is set because a real pending-approval ticket is
			// always GitHub-linked (only createTicketFromIssue produces this
			// phase), and actionStart's guard (fix round 4) requires
			// issue_number IS NOT NULL — an unlinked ticket would always
			// 409 on "start" regardless of phase, which is exercised
			// separately in TestStartActionReleasesPendingApprovalTicket.
			ticket := db.Ticket{RepoRemote: "r", Branch: "b", Description: "d",
				BodyHash: ghsync.HashDescription("d"),
				Phase:    "pending-approval", IssueNumber: &n}
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
			case "start":
				// The dashboard's approval control submits the body_hash
				// its page was rendered from, and the dispatcher requires
				// it (fix round 1c). Supplying the matching hash keeps this
				// case testing what it is here to test — whether "start"
				// may release a pending-approval ticket — rather than
				// stopping at a 400.
				body["reviewed_body_hash"] = ghsync.HashDescription("d")
			}

			raw, err := json.Marshal(body)
			if err != nil {
				t.Fatalf("marshal body: %v", err)
			}
			url := fmt.Sprintf("/api/tickets/%s/actions", ticket.ID)
			req := httptest.NewRequest(http.MethodPost, url, bytes.NewReader(raw))
			req.Header.Set("Content-Type", "application/json")
			withSession(req, cookie)
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
