# Codebase Index

## agents

### [internal/agentrunner](modules/internal_agentrunner.md)

This module abstracts the execution of one-shot AI agent invocations behind a Runner interface, enabling the orchestration core to dispatch role prompts without coupling to a specific backend. It provides a ClaudeCode adapter that shells out to the `claude` CLI, a Mock adapter for test-time scripting, a BuildPrompt function that wraps role prompts and context data in injection-resistant delimiters, and artifact generators that project neutral role files into Claude Code subagent definitions and slash-command skill files.

## blog

### [internal/blog](modules/internal_blog.md)

The blog module implements a ticket's append-only blackboard log — a structured JSONL file where every agent role records timestamped entries during a ticket's lifecycle. It defines the Entry type with its taxonomy of entry types (status, finding, blocker, question, answer, etc.), a mutex-guarded Writer that serialises concurrent appends safely, and a ReadAll reader that tolerates a missing log file as an empty state. The module exists to give all Golem agents a shared, corruption-free audit trail for a single ticket.

## cli

### [cmd/golem](modules/cmd_golem.md)

This is the main entry point for the golem CLI binary. It owns the top-level command dispatch table, groups commands for help output, and routes subcommands (ticket, wiki, log, observer, graph) to their respective handler functions in the internal/cli package. It also embeds the version string injected at release build time and implements the help command with per-command drill-down support.

### [internal/cli](modules/internal_cli.md)

The cli module is the command layer of Golem, exposing every user-facing subcommand as a top-level Go function that accepts parsed flag arguments and io.Writer streams for stdout/stderr, returning an integer exit code. Each command function loads configuration and ticket state, delegates all domain logic to internal packages (ticket, blog, graph, soul, wiki, workspace, observer, gate, askwait, agentrunner), and wires results back to the caller. The module owns no business logic itself; its role is pure orchestration — selecting backends, sequencing cross-package calls, and surfacing errors.

## config

### [internal/config](modules/internal_config.md)

The config module is responsible for loading and validating Golem's configuration from a YAML file. It defines the top-level Config struct and its nested types (GateConfig, PolicyConfig, GraphConfig), exposes a Load function that reads and parses the file while enforcing that the backend field is present, and provides a helper for resolving per-backend ask-and-wait timeout durations. It exists as the single authoritative source of runtime configuration for the rest of the system.

## coordination

### [internal/askwait](modules/internal_askwait.md)

The askwait module implements a question-and-answer protocol over the blog log, allowing one agent role to post a question and another to poll or block-wait for the answer. It enforces a configurable cap on back-and-forth rounds to prevent infinite loops between agents, escalating to human intervention when the limit is reached.

## e2e

### [internal/e2e](modules/internal_e2e.md)

End-to-end test suite that validates the full Golem ticket lifecycle from two angles: a unit-level integration test that exercises the Go API directly using a mock agent backend, and a binary-level test that builds the real golem executable, runs CLI commands against a temporary git repository, and verifies observable side-effects such as log entries and ticket state files. The module exists to catch regressions that only surface when all subsystems are wired together, covering workspace creation, ticket persistence, observer dispatch, and blog log correctness.

## gate

### [internal/gate](modules/internal_gate.md)

The gate module executes a configured sequence of shell commands against a working directory, acting as a quality gate that validates a workspace before proceeding. It runs commands sequentially and short-circuits on the first failure, collecting combined output throughout. It exists to enforce pre-merge or pre-commit checks defined in configuration.

## gating

### [internal/gating](modules/internal_gating.md)

The gating module implements tool-call policy enforcement for Golem agent roles. It exposes a single Evaluate function that acts as the enforcement point (analogous to a PreToolUse hook) before any tool call is permitted to execute. It blocks a hardcoded set of dangerous command prefixes (git push, gh pr create/comment, chmod 777, eval, source), rejects common obfuscation vectors (pipe-to-shell, base64 decode), restricts network calls via curl/wget to an explicit allowlist, and enforces worktree-containment for write_file operations when the policy requires it.

## graph

### [internal/graph](modules/internal_graph.md)

The graph package implements codebase indexing by discovering source files, parsing structured LLM-generated module descriptions, persisting metadata and edges, and writing human-readable wiki documents. It exists to build and maintain a navigable knowledge graph of the repository so that tools and agents can query module relationships, exports, and summaries without re-reading source files from scratch.

## observer

### [internal/observer](modules/internal_observer.md)

The observer module implements the single persistent, deterministic process that mediates between incoming commit signals and one-shot agent invocations. Its core responsibility is preventing duplicate work: it claims each (role, commitSHA) pair in-memory to guard against concurrent goroutines racing within a process, and cross-checks the blog log to skip signals already handled by a prior Observer lifetime. When a signal is genuinely new, it invokes an agent runner, classifies the output as FINDING, BLOCKER, or silence, and appends the result to the structured log.

## roles

### [internal/roles](modules/internal_roles.md)

The roles module bundles the default Golem agent role definitions (markdown files) as embedded assets and provides utilities to unpack them into a target repository's golem directory on first initialisation. It enforces a repo-ownership model where unpacked files are never overwritten on subsequent runs, allowing teams to customise their role definitions freely after the initial `golem init`.

## soul

### [internal/soul](modules/internal_soul.md)

The soul module identifies moments where a human overrode a role's blocker with a different resolution, treating those divergences as candidate soul principles worth promoting. It extracts these candidates from a ticket's blog entries by pairing BLOCKER entries with human RESOLVED replies that differ from the original message, and provides a Promote function to persist accepted principles as markdown files in a designated soul directory.

## ticket

### [internal/ticket](modules/internal_ticket.md)

The ticket module defines the lifecycle state for a Golem development ticket. It models the discrete phases a ticket moves through (brainstorm, plan, approve, implement, review, ready-for-review, needs-attention, close, closed), holds associated metadata such as branch name and worktree path, and provides JSON-backed persistence via Save and Load. New tickets start at PhaseBrainstorm unless marked trivial, in which case they skip directly to PhasePlan.

## tickets

### [internal/bloat](modules/internal_bloat.md)

The bloat package provides a deterministic scope-bloat check for commit diffs. It compares the actual number of changed lines against a plan step's stated expectation and flags the diff as SCOPE_BLOAT when the actual count exceeds a fixed multiplier threshold. When no expected line count is provided, the check abstains, deferring scope judgment to LLM roles.

## wiki

### [internal/wiki](modules/internal_wiki.md)

This module implements a TF-IDF vector search index over wiki documents (markdown files). It exists to provide semantic-quality document retrieval without external API dependencies or credentials — pure Go cosine similarity search that detects near-duplicate code and related modules. The module handles the full lifecycle: loading markdown files from disk with SHA-256 content hashing, building and normalizing TF-IDF vectors, extracting co-mention link edges between documents, searching with optional one-hop link expansion, and persisting/restoring the index to disk via gob encoding. Staleness detection ensures the index is rebuilt whenever wiki content changes.

## workspace

### [internal/workspace](modules/internal_workspace.md)

The workspace module manages Git worktrees for Golem tickets. It provides two operations: creating an isolated worktree and branch for a given ticket ID at a deterministic path under .golem/tickets/, and removing that worktree and branch when the ticket is done. The module shells out to git directly and is tolerant of already-absent worktrees or branches during removal.

