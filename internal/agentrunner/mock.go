package agentrunner

import "fmt"

// Mock is a scripted Runner used in tests so the ticket lifecycle can be
// exercised end-to-end without a real model call or token cost
// (spec: Testing Strategy).
type Mock struct {
	queued map[string][]Result
}

func NewMock() *Mock {
	return &Mock{queued: make(map[string][]Result)}
}

func (m *Mock) ScriptResponse(role string, result Result) {
	m.queued[role] = append(m.queued[role], result)
}

func (m *Mock) WorktreeSetup(_ string) error { return nil }

func (m *Mock) RunAgent(role string, _ Context) (Result, error) {
	q := m.queued[role]
	if len(q) == 0 {
		return Result{}, fmt.Errorf("mock: no scripted response queued for role %q", role)
	}
	result := q[0]
	m.queued[role] = q[1:]
	return result, nil
}
