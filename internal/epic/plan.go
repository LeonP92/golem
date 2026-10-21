// Package epic holds the deterministic core of an Epic Run: the plan
// schema, requirement coverage checks, DAG validation, readiness
// computation and priority ordering. Nothing here talks to GitHub, runs a
// model, or knows how a plan was produced — a plan is data, and every rule
// this package enforces must hold no matter who wrote it.
package epic

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

// MaxNodes bounds a single plan revision. A larger spec is planned in
// rolling waves, not as one unreviewable graph.
const MaxNodes = 200

// Plan is one immutable plan revision. Once approved it never changes; an
// edit is a new Plan with a new hash.
type Plan struct {
	// Requirements are the numbered outcomes the spec demands, extracted
	// before any decomposition. Every requirement must be covered by at
	// least one node, and every node must exist for at least one
	// requirement — no uncovered outcome, no orphan work.
	Requirements []Requirement `json:"requirements"`
	// Nodes are the proposed tickets, in plan order. Plan order is the
	// stable tie-break for scheduling priority.
	Nodes []Node `json:"nodes"`
	// Edges are hard dependencies: the predecessor must be integrated
	// before the successor may start. Soft file-overlap hints are resource
	// constraints, never edges.
	Edges []Edge `json:"edges"`
}

type Requirement struct {
	ID   string `json:"id"`   // stable within the plan, e.g. "R1"
	Text string `json:"text"` // the outcome, in the spec's own terms
}

type Node struct {
	Key                string   `json:"key"` // stable logical key, unique within the plan
	Title              string   `json:"title"`
	Scope              string   `json:"scope"`
	NonGoals           string   `json:"non_goals,omitempty"`
	AcceptanceCommands []string `json:"acceptance_commands"`
	RequirementIDs     []string `json:"requirement_ids"`
	ExpectedModules    []string `json:"expected_modules,omitempty"`
	Risk               string   `json:"risk,omitempty"`
	BranchTarget       string   `json:"branch_target"` // the run's target ref; filled in when empty
}

type Edge struct {
	Predecessor string `json:"predecessor"`
	Successor   string `json:"successor"`
	Reason      string `json:"reason"`
}

// idShape keeps keys and requirement IDs safe for URLs, branch names and
// log lines without quoting surprises.
var idShape = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_.-]*$`)

// Validate checks a plan structurally: well-formed IDs, unique keys, no
// self-edges or unknown endpoints, an acyclic graph, complete requirement
// coverage in both directions, non-empty acceptance criteria and branch
// targets, and bounded size. It returns every problem found, not the
// first — a human reviewing a rejected plan should fix one pass, not five.
func Validate(p Plan) []error {
	var errs []error
	errf := func(format string, args ...any) {
		errs = append(errs, fmt.Errorf(format, args...))
	}

	if len(p.Nodes) == 0 {
		errf("plan has no nodes")
	}
	if len(p.Nodes) > MaxNodes {
		errf("plan has %d nodes, more than the %d a single reviewable revision allows — plan in milestone waves", len(p.Nodes), MaxNodes)
	}

	reqIDs := map[string]bool{}
	for _, r := range p.Requirements {
		if !idShape.MatchString(r.ID) {
			errf("requirement ID %q is not a well-formed identifier", r.ID)
		}
		if reqIDs[r.ID] {
			errf("duplicate requirement ID %q", r.ID)
		}
		reqIDs[r.ID] = true
		if r.Text == "" {
			errf("requirement %s has no text", r.ID)
		}
	}

	keys, lower := map[string]bool{}, map[string]bool{}
	covered := map[string]bool{}
	for _, n := range p.Nodes {
		if !idShape.MatchString(n.Key) {
			errf("node key %q is not a well-formed identifier", n.Key)
		}
		// Case-insensitive: ticket IDs lower-case the key.
		if lower[strings.ToLower(n.Key)] {
			errf("duplicate node key %q", n.Key)
		}
		keys[n.Key], lower[strings.ToLower(n.Key)] = true, true
		if n.Title == "" {
			errf("node %q has no title", n.Key)
		}
		if len(n.AcceptanceCommands) == 0 {
			errf("node %q has no acceptance commands — a ticket with no checkable outcome cannot be verified", n.Key)
		}
		if n.BranchTarget == "" {
			errf("node %q has no branch target", n.Key)
		}
		if len(n.RequirementIDs) == 0 {
			errf("node %q covers no requirement — no ticket without a reason", n.Key)
		}
		for _, rid := range n.RequirementIDs {
			if !reqIDs[rid] {
				errf("node %q references unknown requirement %q", n.Key, rid)
				continue
			}
			covered[rid] = true
		}
	}
	for _, r := range p.Requirements {
		if !covered[r.ID] {
			errf("requirement %s is covered by no node", r.ID)
		}
	}

	for _, e := range p.Edges {
		if !keys[e.Predecessor] {
			errf("edge predecessor %q is not a node", e.Predecessor)
		}
		if !keys[e.Successor] {
			errf("edge successor %q is not a node", e.Successor)
		}
		if e.Predecessor == e.Successor && e.Predecessor != "" {
			errf("node %q depends on itself", e.Predecessor)
		}
		if e.Reason == "" {
			errf("edge %s -> %s has no reason", e.Predecessor, e.Successor)
		}
	}
	if cycle := findCycle(p); cycle != nil {
		errf("dependency cycle: %v", cycle)
	}
	return errs
}

// Hash is the plan's content identity: a SHA-256 over the canonical JSON.
// Approvals bind to this, so any edit — however small — produces a
// different hash and therefore requires a new approval.
func Hash(p Plan) (string, error) {
	data, err := json.Marshal(p)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

// findCycle returns one cycle path if the graph has one, else nil.
// Depth-first over a three-color marking; deterministic because nodes and
// edges are visited in plan order.
func findCycle(p Plan) []string {
	const (
		white = iota // unvisited
		gray         // on the current path
		black        // done
	)
	color := map[string]int{}
	adj := map[string][]string{}
	for _, e := range p.Edges {
		adj[e.Predecessor] = append(adj[e.Predecessor], e.Successor)
	}
	var stack []string
	var cycle []string
	var visit func(key string) bool
	visit = func(key string) bool {
		color[key] = gray
		stack = append(stack, key)
		for _, next := range adj[key] {
			switch color[next] {
			case gray:
				// Found a back edge: the cycle is the stack from next on.
				i := 0
				for i < len(stack) && stack[i] != next {
					i++
				}
				cycle = append(cycle, stack[i:]...)
				return true
			case white:
				if visit(next) {
					return true
				}
			}
		}
		stack = stack[:len(stack)-1]
		color[key] = black
		return false
	}
	for _, n := range p.Nodes {
		if color[n.Key] == white && visit(n.Key) {
			return cycle
		}
	}
	return nil
}

// ApplyBranchTarget fills each node's empty BranchTarget with target and
// reports every node naming any other ref. It is the shared rule for every
// path a plan enters the system by — pasted JSON, a generated draft, the
// validate command — so a plan means the same thing no matter who wrote
// it. Call it before Validate: a node with no branch target is otherwise
// rejected there.
func ApplyBranchTarget(p *Plan, target string) []error {
	var errs []error
	for i, n := range p.Nodes {
		switch n.BranchTarget {
		case "":
			p.Nodes[i].BranchTarget = target
		case target:
		default:
			errs = append(errs, fmt.Errorf("node %q targets %q; this run integrates into %q", n.Key, n.BranchTarget, target))
		}
	}
	return errs
}

// CanonicalPlan parses raw plan JSON, fills empty branch targets with
// target (rejecting any node naming another ref), runs the full structural
// validation, and returns the canonical re-marshalled JSON. It is the one
// gate every plan passes through no matter how it enters the system — the
// epic-plan validate command, a shem checking its generated draft, or the
// orchestrator storing it.
func CanonicalPlan(data []byte, target string) (string, []error) {
	var plan Plan
	if err := json.Unmarshal(data, &plan); err != nil {
		return "", []error{fmt.Errorf("plan JSON does not parse: %w", err)}
	}
	errs := ApplyBranchTarget(&plan, target)
	if errs = append(errs, Validate(plan)...); len(errs) > 0 {
		return "", errs
	}
	canonical, err := json.Marshal(plan)
	if err != nil {
		return "", []error{err}
	}
	return string(canonical), nil
}
