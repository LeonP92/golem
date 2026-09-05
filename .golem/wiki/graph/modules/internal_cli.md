# internal/cli

The cli module is the command layer of Golem, exposing every user-facing subcommand as a top-level Go function that accepts parsed flag arguments and io.Writer streams for stdout/stderr, returning an integer exit code. Each command function loads configuration and ticket state, delegates all domain logic to internal packages (ticket, blog, graph, soul, wiki, workspace, observer, gate, askwait, agentrunner), and wires results back to the caller. The module owns no business logic itself; its role is pure orchestration — selecting backends, sequencing cross-package calls, and surfacing errors.

## Functions

- func TicketAdvance(args []string, stdout, stderr io.Writer) int — advances a ticket to a specified phase by name
- func TicketReview(args []string, stdout, stderr io.Writer) int — runs the reviewer agent on a ticket and sets phase to ready-for-review or needs-attention based on gate outcome
- func NewRunner(cfg *config.Config, worktreeRoot string) (agentrunner.Runner, error) — constructs the appropriate agent runner for the configured backend
- func TicketClose(args []string, stdout, stderr io.Writer) int — closes a ticket by promoting soul entries, updating the graph, tearing down the worktree, and marking phase closed
- func GraphBuild(args []string, stdout, stderr io.Writer) int — discovers all source modules and runs the graph-builder agent in parallel to produce the codebase graph
- func GraphCheckBoundary(args []string, stdout, stderr io.Writer) int — reports modules outside a subsystem that import modules inside it
- func GraphDeps(args []string, stdout, stderr io.Writer) int — prints the recorded imports of a named module from the graph index
- func GraphStatus(args []string, stdout, stderr io.Writer) int — reports graph index staleness by comparing stored file hashes against current content
- func GraphUpdate(args []string, stdout, stderr io.Writer) int — rebuilds only the stale modules in the graph index since the last recorded commit
- func GraphWhoImports(args []string, stdout, stderr io.Writer) int — lists all modules that import a given module
- func Init(args []string, stdout, stderr io.Writer) int — initialises a .golem directory with config, role files, and optional graph build
- func LogEmit(args []string, stdout, stderr io.Writer) int — appends a STATUS, FINDING, BLOCKER, or RESOLVED entry to a ticket's log
- func Ask(args []string, stdout, stderr io.Writer) int — posts a QUESTION to a ticket log and blocks until an ANSWER arrives or times out
- func Answer(args []string, stdout, stderr io.Writer) int — appends an ANSWER entry to a ticket log in reply to a prior question
- func ObserverDispatch(args []string, stdout, stderr io.Writer) int — dispatches a watcher role agent for a specific commit diff and records findings
- func TicketResume(args []string, stdout, stderr io.Writer) int — prints ticket phase, branch, and last log entry for a given ticket
- func SetStep(args []string, stdout, stderr io.Writer) int — records the expected diff line count for the next implementation step on a ticket
- func CheckBloat(args []string, stdout, stderr io.Writer) int — checks a commit's changed line count against the step expectation and logs a SCOPE_BLOAT finding if exceeded
- func TicketNew(args []string, stdout, stderr io.Writer) int — creates a new ticket with a worktree, branch, and initial log entry; accepts --ticket-id (intended for Shem workers) as an alternative to --id; if both flags are provided, --ticket-id takes precedence and overrides --id
- func Tickets(args []string, stdout, stderr io.Writer) int — lists all tickets with their id, phase, and branch
- func WikiSearch(args []string, stdout, stderr io.Writer) int — searches the wiki index for documents matching a query
- func WikiRebuild(args []string, stdout, stderr io.Writer) int — rebuilds the TF-IDF wiki index from all wiki documents on disk

## Imports

internal/agentrunner, internal/blog, internal/config, internal/gate, internal/ticket, internal/graph, internal/soul, internal/workspace, internal/observer, internal/askwait, internal/bloat, internal/roles, internal/wiki
