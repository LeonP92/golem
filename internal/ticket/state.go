package ticket

import (
	"encoding/json"
	"os"
	"path/filepath"
)

type Phase string

const (
	PhaseBrainstorm      Phase = "brainstorm"
	PhasePlan            Phase = "plan"
	PhaseApprove         Phase = "approve"
	PhaseImplement       Phase = "implement"
	PhaseReview          Phase = "review"
	PhaseReadyForReview  Phase = "ready-for-review"
	PhaseNeedsAttention  Phase = "needs-attention"
	PhaseClose           Phase = "close"
	PhaseClosed          Phase = "closed"
)

type State struct {
	ID                       string `json:"id"`
	Description              string `json:"description"`
	Phase                    Phase  `json:"phase"`
	Branch                   string `json:"branch"`
	WorktreePath             string `json:"worktree_path"`
	Trivial                  bool   `json:"trivial"`
	CurrentStepExpectedLines int    `json:"current_step_expected_lines"`
}

// New skips straight to PhasePlan for trivial tickets (spec: Ticket
// Lifecycle, step 1) — everything else starts at PhaseBrainstorm.
func New(id, description string, trivial bool) *State {
	phase := PhaseBrainstorm
	if trivial {
		phase = PhasePlan
	}
	return &State{
		ID:          id,
		Description: description,
		Phase:       phase,
		Trivial:     trivial,
	}
}

func stateFilePath(ticketDir string) string {
	return filepath.Join(ticketDir, "state.json")
}

func (s *State) Save(ticketDir string) error {
	if err := os.MkdirAll(ticketDir, 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(stateFilePath(ticketDir), data, 0o644)
}

func Load(ticketDir string) (*State, error) {
	data, err := os.ReadFile(stateFilePath(ticketDir))
	if err != nil {
		return nil, err
	}
	var s State
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, err
	}
	return &s, nil
}
