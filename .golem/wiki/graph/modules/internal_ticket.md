# internal/ticket

The ticket module defines the lifecycle state for a Golem development ticket. It models the discrete phases a ticket moves through (brainstorm, plan, approve, implement, review, ready-for-review, needs-attention, close, closed), holds associated metadata such as branch name and worktree path, and provides JSON-backed persistence via Save and Load. New tickets start at PhaseBrainstorm unless marked trivial, in which case they skip directly to PhasePlan.

## Functions

- func New(id, description string, trivial bool) *State — constructs a new State with defaults, skipping brainstorm for trivial tickets
- func (s *State) Save(ticketDir string) error — serialises State to JSON in the given directory
- func Load(ticketDir string) (*State, error) — deserialises State from JSON in the given directory

## Types

- Phase — string enum representing a ticket's current lifecycle phase
- State — holds all persistent fields for a ticket (ID, description, phase, branch, worktree path, trivial flag, expected lines)
