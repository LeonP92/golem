package agentrunner

import "testing"

func TestMockReturnsScriptedResponse(t *testing.T) {
	m := NewMock()
	m.ScriptResponse("reviewer", Result{Output: "LGTM", Model: "mock-model"})

	result, err := m.RunAgent("reviewer", Context{})
	if err != nil {
		t.Fatalf("RunAgent: %v", err)
	}
	if result.Output != "LGTM" {
		t.Errorf("Output = %q, want LGTM", result.Output)
	}
}

func TestMockErrorsOnUnscriptedRole(t *testing.T) {
	m := NewMock()
	_, err := m.RunAgent("developer", Context{})
	if err == nil {
		t.Fatal("expected error for a role with no scripted response")
	}
}

func TestMockPopsQueuedResponsesInOrder(t *testing.T) {
	m := NewMock()
	m.ScriptResponse("developer", Result{Output: "first"})
	m.ScriptResponse("developer", Result{Output: "second"})

	r1, _ := m.RunAgent("developer", Context{})
	r2, _ := m.RunAgent("developer", Context{})
	if r1.Output != "first" || r2.Output != "second" {
		t.Errorf("got %q then %q, want first then second", r1.Output, r2.Output)
	}
}
