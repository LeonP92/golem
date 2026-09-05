package observer

import (
	"strings"
	"sync"

	"github.com/leonp92/golem/internal/agentrunner"
	"github.com/leonp92/golem/internal/blog"
)

// Observer is the single persistent, deterministic process per ticket.
// It claims signals before dispatching a one-shot agent invocation for
// them, preventing two goroutines from ever answering the same signal
// twice (spec: Concurrency Model — signal claiming, commit coalescing).
//
// `claimed` is in-memory only and does not survive an observer restart.
// Resume-safety instead comes from DispatchForCommit
// checking the log itself before claiming — a FINDING/BLOCKER already
// logged for a role+commitSHA is proof the work happened in a prior run.
// The in-memory map is only a fast-path guard against two goroutines
// racing within the same process.
type Observer struct {
	logPath string
	runner  agentrunner.Runner

	mu      sync.Mutex
	claimed map[string]bool
}

func New(logPath string, runner agentrunner.Runner) *Observer {
	return &Observer{
		logPath: logPath,
		runner:  runner,
		claimed: make(map[string]bool),
	}
}

func claimKey(role, commitSHA string) string {
	return role + "@" + commitSHA
}

// IsInFlight reports whether a dispatch for this role+commit is already
// claimed (running or completed within this Observer's lifetime).
func (o *Observer) IsInFlight(role, commitSHA string) bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.claimed[claimKey(role, commitSHA)]
}

func alreadyLogged(entries []blog.Entry, role, commitSHA string) bool {
	for _, e := range entries {
		if e.Role == role && e.CommitSHA == commitSHA && (e.Type == blog.TypeFinding || e.Type == blog.TypeBlocker) {
			return true
		}
	}
	return false
}

// DispatchForCommit claims the (role, commitSHA) pair and, if it wasn't
// already claimed — in memory this run, or in the log from a prior run —
// runs a one-shot invocation and appends the result to the log. This is
// what prevents duplicate FINDING/BLOCKER entries for one signal, both
// across racing goroutines and across an observer restart.
func (o *Observer) DispatchForCommit(role, commitSHA, diff, rolePrompt string) error {
	key := claimKey(role, commitSHA)

	o.mu.Lock()
	if o.claimed[key] {
		o.mu.Unlock()
		return nil
	}
	o.claimed[key] = true
	o.mu.Unlock()

	entries, err := blog.ReadAll(o.logPath)
	if err != nil {
		return err
	}
	if alreadyLogged(entries, role, commitSHA) {
		return nil
	}

	result, err := o.runner.RunAgent(role, agentrunner.Context{
		Diff:       diff,
		RolePrompt: rolePrompt,
	})
	if err != nil {
		return err
	}

	entryType := classify(result.Output)
	if entryType == "" {
		return nil // silence is a valid outcome (spec: Roles — staying in scope)
	}

	w, err := blog.NewWriter(o.logPath)
	if err != nil {
		return err
	}
	defer w.Close()

	e := blog.NewEntry(role, entryType, result.Output)
	e.CommitSHA = commitSHA
	e.Model = result.Model
	return w.Append(e)
}

func classify(output string) blog.EntryType {
	switch {
	case strings.HasPrefix(output, "BLOCKER:"):
		return blog.TypeBlocker
	case strings.HasPrefix(output, "FINDING:"):
		return blog.TypeFinding
	default:
		return ""
	}
}
