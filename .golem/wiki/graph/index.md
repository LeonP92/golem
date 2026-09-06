# Codebase Index

## agents

### [internal/agentrunner](modules/internal_agentrunner.md)

This module abstracts the execution of one-shot AI agent invocations behind a Runner interface, enabling the orchestration core to dispatch role prompts without coupling to a specific backend. It provides a ClaudeCode adapter that shells out to the `claude` CLI, a Mock adapter for test-time scripting, a BuildPrompt function that wraps role prompts and context data in injection-resistant delimiters, and artifact generators that project neutral role files into Claude Code subagent definitions and slash-command skill files.

## api

### [internal/orchestrator/api](modules/internal_orchestrator_api.md)

This module implements the HTTP API layer for the Golem orchestrator server, exposing REST endpoints consumed by both Shem agents (authenticated via API key) and human operators (authenticated via session cookie). It is organized into four route groups: ticket lifecycle management (create, list, get, claim, phase/checkpoint updates), Shem registration and WebSocket upgrade, log ingestion and SSE streaming, and human-input CRUD plus a unified ticket action dispatcher. The Handlers struct is the central dependency carrier holding a GORM database handle, a WebSocket hub for pushing real-time messages to connected Shems, and an SSE broker for streaming log events to browser clients. All mutation endpoints enforce ownership or phase preconditions before writing, and most state changes fan out notifications to relevant Shems via WebSocket broadcast or push.

### [internal/orchestrator/server](modules/internal_orchestrator_server.md)

The server module is the HTTP wiring layer for the Golem orchestrator. It holds shared dependencies (database, WebSocket hub, SSE broker) in a Server struct and assembles the full request mux by delegating to the api, ui, and ws subpackages. It exists as the single composition root that binds all handler groups — Shem-facing API routes, ticket and log endpoints, human dashboard routes, and a health check — into one http.Handler returned to main.

### [internal/orchestrator/sse](modules/internal_orchestrator_sse.md)

The sse module implements a fan-out event broker for Server-Sent Events within the orchestrator. It defines a LogEntryEvent type carrying structured log entry data (sequence number, entry type, role pair, and message), and a Broker that maintains per-ticket subscriber channels. Clients subscribe to a ticket's event stream and receive a cancel function to unsubscribe; the broker publishes events to all registered subscribers for a given ticket, dropping events silently when a subscriber's buffer is full rather than blocking.

### [internal/orchestrator/ui](modules/internal_orchestrator_ui.md)

This module provides the HTTP handler layer and HTML template engine for the Golem Orchestrator web dashboard. It embeds all templates in the binary via go:embed, parses them into a page-keyed map at startup, and exposes route handlers for login/logout, the ticket dashboard, shem listing, ticket creation, and ticket detail views. It enforces session authentication via middleware on protected routes, renders server-side HTML using a layout-plus-page-plus-partials template composition model, and provides an SSE-compatible log entry renderer that converts structured log events to HTML fragments for live streaming to the browser.

## auth

### [internal/orchestrator/auth](modules/internal_orchestrator_auth.md)

This module provides two independent HTTP authentication mechanisms for the Golem orchestrator: API key authentication for machine-to-machine shem node requests (Bearer token validated against bcrypt hashes stored in the database), and session-based authentication for human users accessing the web UI (random token stored as SHA-256 hash in the database with a 7-day TTL, delivered via HTTP-only cookie). Both mechanisms inject their authenticated principal into the request context for downstream handlers to retrieve.

### [internal/orchestrator/admin](modules/internal_orchestrator_admin.md)

The admin module provides CLI-callable helper functions for managing orchestrator users and shems (agent API keys). It handles interactive and non-interactive password collection, bcrypt hashing, and CRUD operations against the database via GORM — covering creation, upsert, and hard-deletion of both User and Shem records. It exists as the administrative control plane for the orchestrator's credential store, used during initial setup, Docker bootstrap, and day-to-day user management.

## blog

### [internal/blog](modules/internal_blog.md)

The blog module implements a ticket's append-only blackboard log — a structured JSONL file where every agent role records timestamped entries during a ticket's lifecycle. It defines the Entry type with its taxonomy of entry types (status, finding, blocker, question, answer, timeout, blocked tool call, and resolved), a mutex-guarded Writer that serialises concurrent appends safely, and a ReadAll reader that tolerates a missing log file as an empty state. The module exists to give all Golem agents a shared, corruption-free audit trail for a single ticket.

## cli

### [internal/cli](modules/internal_cli.md)

The cli module is the command layer of Golem, exposing every user-facing subcommand as a top-level Go function that accepts parsed flag arguments and io.Writer streams for stdout/stderr, returning an integer exit code. Each command function loads configuration and ticket state, delegates all domain logic to internal packages (ticket, blog, graph, soul, wiki, workspace, observer, gate, askwait, agentrunner), and wires results back to the caller. The module owns no business logic itself; its role is pure orchestration — selecting backends, sequencing cross-package calls, and surfacing errors.

### [cmd/golem](modules/cmd_golem.md)

This is the main entry point for the golem CLI binary. It owns the top-level command dispatch table, groups commands for help output, and routes subcommands (ticket, wiki, log, observer, graph) to their respective handler functions in the internal/cli package. It also embeds the version string injected at release build time and implements the help command with per-command drill-down support.

## config

### [internal/config](modules/internal_config.md)

The config module is responsible for loading and validating Golem's configuration from a YAML file. It defines the top-level Config struct and its nested types (GateConfig, PolicyConfig, GraphConfig), exposes a Load function that reads and parses the file while enforcing that the backend field is present, and provides a helper for resolving per-backend ask-and-wait timeout durations. It exists as the single authoritative source of runtime configuration for the rest of the system.

### [internal/orchestrator/config](modules/internal_orchestrator_config.md)

This module loads and parses the orchestrator server's YAML configuration file. It defines the Config struct (port, database path, session secret, and TLS settings) and the nested TLSConfig struct, and exposes a single Load function that reads a file from disk and unmarshals it into a Config value. It exists as the authoritative source of runtime configuration for the orchestrator server.

### [internal/shem/config](modules/internal_shem_config.md)

This module loads and validates shem-node configuration from a YAML file (shem.yaml). It defines the Config struct holding orchestrator URL, API key, node name, managed repository list, and operational flags (NoPush, MaxConcurrent), as well as the RepoConfig struct for individual repository entries. The Load function reads the file, unmarshals YAML, normalizes repository remote URLs via urlnorm, and allows environment variables (GOLEM_SHEM_API_KEY, GOLEM_SHEM_NAME) to override file-level values for Docker-friendly deployments.

## coordination

### [internal/shem/client](modules/internal_shem_client.md)

This module provides the HTTP and WebSocket client layer that a shem (worker node) uses to communicate with the Golem orchestrator server. The HTTP client wraps all REST API calls behind a retry-on-5xx policy with exponential back-off, covering shem registration/deregistration, ticket claiming, phase and checkpoint updates, structured log posting, document file streaming, and human-input polling and acknowledgement. The WebSocket client establishes a push channel to receive real-time orchestrator events, maintains connection liveness via periodic heartbeat pings, and dispatches decoded messages to a caller-supplied handler.

### [internal/askwait](modules/internal_askwait.md)

The askwait module implements a question-and-answer coordination protocol over the shared blog log, allowing one agent role to post a typed QUESTION entry and another to poll or block-wait until a matching ANSWER entry appears. It generates random IDs to correlate question/answer pairs, enforces a configurable round cap (MaxRounds=3) to prevent agents from looping indefinitely, and provides both a non-blocking poll and a timeout-bounded blocking wait so callers can choose their own concurrency model.

## e2e

### [internal/e2e](modules/internal_e2e.md)

End-to-end test suite that validates the full Golem ticket lifecycle from three angles: a unit-level integration test exercising the Go API directly with a mock agent backend, a binary-level test that builds the real golem executable and runs CLI commands against a temporary git repository, and an orchestrator test that exercises concurrent ticket claiming, heartbeat-based shem recovery, and SSE log forwarding using an in-memory SQLite database. The module exists to catch regressions that only surface when all subsystems are wired together, covering workspace creation, ticket persistence, observer dispatch, blog log correctness, and orchestrator coordination primitives.

## gate

### [internal/gate](modules/internal_gate.md)

The gate module executes a configured sequence of shell commands against a working directory, acting as a quality gate that validates a workspace before proceeding. It runs commands sequentially and short-circuits on the first failure, collecting combined output throughout. It exists to enforce pre-merge or pre-commit checks defined in configuration.

## graph

### [internal/graph](modules/internal_graph.md)

The graph package implements codebase indexing by discovering source files, parsing structured LLM-generated module descriptions, persisting metadata and edges, and writing human-readable wiki documents. It exists to build and maintain a navigable knowledge graph of the repository so that tools and agents can query module relationships, exports, and summaries without re-reading source files from scratch. The package combines tree-sitter-based structural extraction (imports, exported functions and types) across Go, Python, TypeScript, JavaScript, Rust, and Java with an LLM cache backed by atomic JSON persistence to avoid redundant API calls when file contents are unchanged.

## observer

### [internal/observer](modules/internal_observer.md)

The observer module implements the single persistent, deterministic process that mediates between incoming commit signals and one-shot agent invocations. Its core responsibility is preventing duplicate work: it claims each (role, commitSHA) pair in-memory to guard against concurrent goroutines racing within a process, and cross-checks the blog log to skip signals already handled by a prior Observer lifetime. When a signal is genuinely new, it invokes an agent runner, classifies the output as FINDING, BLOCKER, or silence, and appends the result to the structured log.

## orchestration

### [cmd/shem](modules/cmd_shem.md)

This is the entry point for the shem worker binary — a lightweight agent process that connects to a Golem orchestrator, receives task assignments, and executes them via a GolemExecutor. On startup it loads shem.yaml configuration, constructs an HTTP client and worker, then establishes a WebSocket connection to the orchestrator with exponential-backoff reconnection logic. It falls back gracefully to polling if WebSocket is unavailable. The process runs until it receives SIGTERM or SIGINT, at which point it shuts down cleanly.

### [internal/shem/worker](modules/internal_shem_worker.md)

The worker module implements the Golem shem (agent daemon) execution layer. It contains three cooperating components: a Worker that polls the orchestrator for available tickets, claims them with concurrency control, and dispatches them via a pluggable Executor interface; a GolemExecutor that drives each ticket through brainstorm → plan → implement phases by invoking `claude --print` sessions, posting checkpoints and approval requests to the orchestrator, and tailing the ticket log for real-time forwarding; and a RecoverTicket subsystem that reconstructs local ticket state from a ClaimResponse checkpoint so that shem restarts can resume mid-flight tickets without data loss.

## orchestrator

### [internal/orchestrator/ws](modules/internal_orchestrator_ws.md)

This module implements the WebSocket connection layer for the Golem orchestrator, providing a thread-safe Hub that tracks active shem connections keyed by shem ID, supports targeted Push and repo-scoped Broadcast message delivery, and runs a background heartbeat monitor that detects dead shems (those that have not sent a heartbeat within a configurable timeout), marks them offline in the database, and returns their in-progress tickets to the unassigned pool.

### [cmd/orchestrator](modules/cmd_orchestrator.md)

This is the main entry point for the orchestrator server binary. It handles two distinct execution modes: an admin CLI mode for managing users and shem API keys directly against the database (subcommands `users add/remove` and `shems add/remove`), and a normal HTTP server mode that loads configuration, auto-provisions admin and shem credentials from environment variables, wires together the WebSocket hub, SSE broker, and heartbeat monitor, then listens for connections with optional TLS.

### [internal/orchestrator/urlnorm](modules/internal_orchestrator_urlnorm.md)

The urlnorm module provides a single canonicalization function for git remote URLs. It lowercases the scheme and host, strips trailing ".git" suffixes and trailing slashes, and removes query strings and fragments so that two URLs pointing to the same repository always compare equal. The module exists to give the orchestrator a reliable identity key for remote repositories regardless of how the URL was originally typed or cloned.

## roles

### [internal/roles](modules/internal_roles.md)

The roles module bundles the default Golem agent role definitions (markdown files) as embedded assets and provides utilities to unpack them into a target repository's golem directory on first initialisation. It enforces a repo-ownership model where unpacked files are never overwritten on subsequent runs, allowing teams to customise their role definitions freely after the initial `golem init`.

## security

### [internal/gating](modules/internal_gating.md)

The gating module implements tool-call policy enforcement for Golem agent roles. It exposes a single Evaluate function that acts as the enforcement point (analogous to a PreToolUse hook) before any tool call is permitted to execute. It blocks a hardcoded set of dangerous command prefixes (git push, gh pr create/comment, chmod 777, eval, source), rejects common obfuscation vectors (pipe-to-shell, base64 decode), restricts network calls via curl/wget to an explicit allowlist, and enforces worktree-containment for write_file operations when the policy requires it.

## soul

### [internal/soul](modules/internal_soul.md)

The soul module identifies moments where a human overrode a role's blocker with a different resolution, treating those divergences as candidate soul principles worth promoting. It extracts these candidates from a ticket's blog entries by pairing BLOCKER entries with human RESOLVED replies that differ from the original message, and provides a Promote function to persist accepted principles as markdown files in a designated soul directory.

## storage

### [internal/orchestrator/db](modules/internal_orchestrator_db.md)

This module provides database connectivity and schema management for the orchestrator server. It exposes a single Open function that selects between PostgreSQL and SQLite drivers based on the DSN prefix, runs GORM AutoMigrate to create or update all model tables, and enables WAL journal mode for SQLite to allow concurrent access. The models it defines — User, Session, Shem, Ticket, LogEntry, and HumanInput — represent the full persistent state of the orchestrator: web UI users and their sessions, registered Shem worker agents, work tickets and their lifecycle phase, structured log entries produced by Shems, and human-input requests that block ticket progress pending operator response.

## ticket

### [internal/ticket](modules/internal_ticket.md)

The ticket module defines the lifecycle state for a Golem development ticket. It models the discrete phases a ticket moves through (brainstorm, plan, approve, implement, review, ready-for-review, needs-attention, close, closed), holds associated metadata such as branch name and worktree path, and provides JSON-backed persistence via Save and Load. New tickets start at PhaseBrainstorm unless marked trivial, in which case they skip directly to PhasePlan.

## tickets

### [internal/bloat](modules/internal_bloat.md)

The bloat package provides a deterministic scope-bloat check for commit diffs. It compares the actual number of changed lines against a plan step's stated expectation and flags the diff as SCOPE_BLOAT when the actual count exceeds a fixed multiplier threshold. When no expected line count is provided, the check abstains, deferring scope judgment to LLM roles.

## wiki

### [internal/wiki](modules/internal_wiki.md)

This module implements a TF-IDF vector search index over wiki documents (markdown files), providing semantic-quality document retrieval without external API dependencies or credentials. It handles the full lifecycle: loading markdown files from disk with SHA-256 content hashing via LoadAll and Hash, building and normalizing TF-IDF vectors with co-mention link edge extraction, searching with optional one-hop link expansion via SearchExpanded, and persisting/restoring the index to disk via gob encoding. Staleness detection in EnsureIndex ensures the index is rebuilt whenever wiki content changes, making it the single entry point for callers that need an always-fresh index.

## workspace

### [internal/workspace](modules/internal_workspace.md)

The workspace module manages Git worktrees for Golem tickets. It provides two operations: creating an isolated worktree and branch for a given ticket ID at a deterministic path under .golem/tickets/, and removing that worktree and branch when the ticket is done. The module shells out to git directly and is tolerant of already-absent worktrees or branches during removal.

