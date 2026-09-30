package agentrunner

import (
	"context"
	"fmt"
	"io"
)

// MockCall is one recorded invocation.
type MockCall struct {
	Role  string
	Model string
	Phase bool
}

// Mock is a scripted Runner used in tests so the ticket lifecycle can be
// exercised end-to-end without a real model call or token cost
// (spec: Testing Strategy).
type Mock struct {
	queued map[string][]Result

	// Calls is every invocation in order.
	Calls []MockCall
}

func NewMock() *Mock {
	return &Mock{queued: make(map[string][]Result)}
}

func (m *Mock) ScriptResponse(role string, result Result) {
	m.queued[role] = append(m.queued[role], result)
}

func (m *Mock) Name() string                                      { return "mock" }
func (m *Mock) ReservedArgs() []string                            { return nil }
func (m *Mock) WorktreeSetup(string) error                        { return nil }
func (m *Mock) PrepareHost(string) error                          { return nil }
func (m *Mock) GenerateArtifacts(string, map[string]string) error { return nil }

func (m *Mock) RunAgent(_ context.Context, role string, _ Context, model string) (Result, error) {
	m.Calls = append(m.Calls, MockCall{Role: role, Model: model})
	q := m.queued[role]
	if len(q) == 0 {
		return Result{}, fmt.Errorf("mock: no scripted response queued for role %q", role)
	}
	result := q[0]
	m.queued[role] = q[1:]
	return result, nil
}

func (m *Mock) RunPhase(_ context.Context, _, _ string, out io.Writer, model string) error {
	m.Calls = append(m.Calls, MockCall{Model: model, Phase: true})
	_, err := io.WriteString(out, "mock phase output\n")
	return err
}
