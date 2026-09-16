# Codebase Index

## agents

### [internal/agentrunner](modules/internal_agentrunner.md)

The agentrunner module abstracts one-shot AI agent invocations behind a Runner interface so the orchestration core never depends on which backend is configured. It supplies a ClaudeCode adapter that shells out to the host's `claude --print` CLI (storing no credentials of its own) and generates Claude Code projection artifacts — subagent definition files under .claude/agents, a .claude/settings.json whose permission allow/deny lists are merged into any pre-existing file without dropping unrelated top-level fields, per-worktree settings written by WorktreeSetup, and slash-command skill files for the new-ticket and tickets workflows. It also supplies a Mock adapter that pops scripted per-role responses so the ticket lifecycle can be exercised end-to-end without token cost, and a shared BuildPrompt helper that every flat-text CLI-shim backend should use: it wraps log entries and diffs in explicit data-not-instruction delimiters, keeping injection-mitigation behaviour identical across adapters rather than drifting per backend.

## api

### [internal/orchestrator/server](modules/internal_orchestrator_server.md)

The server module is the HTTP wiring layer for the Golem orchestrator. It holds shared dependencies (database, WebSocket hub, SSE broker, secure-cookie flag) in a Server struct and assembles the full request mux by delegating to the api, ui, and ws subpackages. It exists as the single composition root that binds all handler groups — Shem-facing API routes, ticket and log endpoints, human dashboard routes, and a health check — into one http.Handler returned to main.

### [internal/orchestrator/sse](modules/internal_orchestrator_sse.md)

The sse module implements a fan-out event broker for Server-Sent Events within the orchestrator. It defines a LogEntryEvent type carrying structured log entry data (sequence number, entry type, role pair, and message), and a Broker that maintains per-ticket subscriber channels. Clients subscribe to a ticket's event stream and receive a cancel function to unsubscribe; the broker publishes events to all registered subscribers for a given ticket, dropping events silently when a subscriber's buffer is full rather than blocking.

## auth

### [internal/orchestrator/admin](modules/internal_orchestrator_admin.md)

The admin module provides the CLI- and bootstrap-callable control plane for the orchestrator's credential store, managing both human users and shems (agent nodes with API keys) directly against the database via GORM. For users it validates role strings through the rbac package, collects passwords interactively via terminal prompt or non-interactively from the GOLEM_ADMIN_PASSWORD environment variable for Docker setups, bcrypt-hashes them, and supports create, list, role change, upsert, and hard-delete operations. Deletion and demotion are guarded by a last-admin check that refuses any operation which would leave the orchestrator with zero admins, and removing a user also deletes their sessions so access is revoked immediately rather than lingering until token expiry. For shems it generates random 32-byte hex API keys printed once to stdout, stores only their bcrypt hashes, and supports upsert and removal for env-var-driven auto-provisioning at server startup.

### [internal/orchestrator/auth](modules/internal_orchestrator_auth.md)

This module provides two independent HTTP authentication mechanisms for the Golem orchestrator: API key authentication for machine-to-machine shem node requests, validating an "X-Shem-Name" header plus a Bearer token against a bcrypt hash stored on the db.Shem record, and session-based authentication for human web UI users, issuing a random 32-byte token whose SHA-256 hash is stored in db.Session with a 7-day TTL and delivered via an HTTP-only "golem_session" cookie. Both mechanisms are implemented as net/http middleware that inject their authenticated principal (a *db.Shem or *db.User) into the request context via unexported context keys, with accessor functions to retrieve the principal in downstream handlers; API key failures return 401 while session failures redirect to /login.

### [internal/orchestrator/rbac](modules/internal_orchestrator_rbac.md)

The rbac module defines the orchestrator's role-based access control policy as a compile-time table rather than a runtime policy engine. It declares the Role and Permission string types, the known roles (admin, developer), the six permission constants covering user management, shem registration and viewing, and ticket creation/viewing/management, and a single rolePermissions map that is the sole source of truth for which role holds which permission, denying by default for any role absent from the table. On top of that table it exposes Roles for stable display ordering in UI dropdowns and CLI messages, ParseRole for validating role strings on every write path, Can as the one seam every authorization decision flows through (nil users and unknown roles hold nothing), and Require, an HTTP middleware that must be nested inside auth.RequireSession and refuses a request with 403 when the session user in the request context lacks the required permission.

## blog

### [internal/blog](modules/internal_blog.md)

The blog module implements a ticket's append-only blackboard log — a structured JSONL file where every agent role records timestamped entries during a ticket's lifecycle. It defines the Entry type with its taxonomy of entry types (status, finding, blocker, question, answer, timeout, blocked tool call, and resolved), a mutex-guarded Writer that serialises concurrent appends safely, and a ReadAll reader that tolerates a missing log file as an empty state. The module exists to give all Golem agents a shared, corruption-free audit trail for a single ticket.

## cli

### [cmd/golem](modules/cmd_golem.md)

This is the main entry point for the golem CLI binary. It owns the top-level command dispatch table, groups commands for help output, and routes subcommands (ticket, wiki, log, observer, graph) to their respective handler functions in the internal/cli package. It also embeds the version string injected at release build time and implements the help command with per-command drill-down support.

### [internal/cli](modules/internal_cli.md)

The cli module is the command layer of Golem, exposing every user-facing subcommand as a top-level Go function that accepts parsed flag arguments and io.Writer streams for stdout/stderr, returning an integer exit code. Each command function loads configuration and ticket state, delegates all domain logic to internal packages (ticket, blog, graph, soul, wiki, workspace, observer, gate, askwait, agentrunner), and wires results back to the caller. It also implements the code-graph build/update/query subcommands, running structural tree-sitter extraction alongside cached LLM summarization to produce and maintain the wiki graph index. The module owns no business logic itself; its role is pure orchestration — selecting backends, sequencing cross-package calls, and surfacing errors.

## config

### [internal/config](modules/internal_config.md)

The config module is responsible for loading and validating Golem's configuration from a YAML file. It defines the top-level Config struct and its nested types (GateConfig, PolicyConfig, GraphConfig), exposes a Load function that reads and parses the file while enforcing that the backend field is present, and provides a helper for resolving per-backend ask-and-wait timeout durations. It exists as the single authoritative source of runtime configuration for the rest of the system.

### [internal/orchestrator/config](modules/internal_orchestrator_config.md)

This module loads and parses the orchestrator server's YAML configuration file. It defines the Config struct (port, database path, session secret, and TLS settings) and the nested TLSConfig struct, and exposes a single Load function that reads a file from disk and unmarshals it into a Config value. It exists as the authoritative source of runtime configuration for the orchestrator server.

### [internal/shem/config](modules/internal_shem_config.md)

This module loads and validates shem-node configuration from a YAML file (shem.yaml). It defines the Config struct holding orchestrator URL, API key, node name, managed repository list, and operational flags (NoPush, MaxConcurrent), as well as the RepoConfig struct for individual repository entries. The Load function reads the file, unmarshals YAML, normalizes repository remote URLs via urlnorm, and allows environment variables (GOLEM_SHEM_API_KEY, GOLEM_SHEM_NAME) to override file-level values for Docker-friendly deployments.

## coordination

### [internal/askwait](modules/internal_askwait.md)

The askwait module implements a question-and-answer coordination protocol over the shared blog log, allowing one agent role to post a typed QUESTION entry and another to poll or block-wait until a matching ANSWER entry appears. It generates random IDs to correlate question/answer pairs, enforces a configurable round cap (MaxRounds=3) to prevent agents from looping indefinitely, and provides both a non-blocking poll and a timeout-bounded blocking wait so callers can choose their own concurrency model.

### [internal/shem/client](modules/internal_shem_client.md)

This module provides the HTTP and WebSocket client layer that a shem (worker node) uses to communicate with the Golem orchestrator server. The HTTP client wraps all REST API calls behind a retry-on-5xx policy with exponential back-off, covering shem registration/deregistration, ticket claiming (including resuming revising-phase tickets via revise-claim), phase and checkpoint updates, structured log posting, document file streaming, and human-input polling and acknowledgement. The WebSocket client establishes a push channel to receive real-time orchestrator events, maintains connection liveness via periodic heartbeat pings, and dispatches decoded messages to a caller-supplied handler.

## gate

### [internal/gate](modules/internal_gate.md)

The gate module executes a configured sequence of shell commands against a working directory, acting as a quality gate that validates a workspace before proceeding. It runs commands sequentially and short-circuits on the first failure, collecting combined output throughout. It exists to enforce pre-merge or pre-commit checks defined in configuration.

## graph

### [internal/graph](modules/internal_graph.md)

The graph package implements codebase indexing by discovering source files, parsing structured LLM-generated module descriptions, persisting metadata and edges, and writing human-readable wiki documents. It exists to build and maintain a navigable knowledge graph of the repository so that tools and agents can query module relationships, exports, and summaries without re-reading source files from scratch. The package combines tree-sitter-based structural extraction (imports, exported functions and types) across Go, Python, TypeScript, JavaScript, Rust, and Java with an LLM cache backed by atomic JSON persistence to avoid redundant API calls when file contents are unchanged.

## misc

### [internal/orchestrator/api](modules/internal_orchestrator_api.md)



## observer

### [internal/observer](modules/internal_observer.md)

The observer module implements the single persistent, deterministic process that mediates between incoming commit signals and one-shot agent invocations. Its core responsibility is preventing duplicate work: it claims each (role, commitSHA) pair in-memory to guard against concurrent goroutines racing within a process, and cross-checks the blog log to skip signals already handled by a prior Observer lifetime. When a signal is genuinely new, it invokes an agent runner, classifies the output as FINDING, BLOCKER, or silence, and appends the result to the structured log.

## orchestration

### [cmd/orchestrator](modules/cmd_orchestrator.md)

This is the main entry point for the orchestrator server binary. It handles two distinct execution modes: an admin CLI mode that operates directly on the database and exits, supporting role-aware user management ("users add [--role admin|developer]", "users list", "users set-role", "users remove") and shem API key registration ("shems add --name", "shems remove"); and a normal HTTP server mode that loads YAML configuration, opens the database, auto-provisions an admin user (with the admin role) and a shem API key from environment variables (GOLEM_ADMIN_PASSWORD/GOLEM_ADMIN_USERNAME and GOLEM_SHEM_API_KEY/GOLEM_SHEM_NAME) on every startup, wires together the WebSocket hub, SSE broker, and a background heartbeat monitor, derives the secure-cookie flag from whether TLS is configured, and then listens for HTTP or HTTPS connections accordingly.

### [cmd/shem](modules/cmd_shem.md)

This is the entry point for the shem worker binary — a lightweight agent process that connects to a Golem orchestrator, receives task assignments, and executes them via a GolemExecutor. On startup it loads shem.yaml configuration, constructs an HTTP client and worker, then establishes a WebSocket connection to the orchestrator with exponential-backoff reconnection logic, falling back gracefully to polling if WebSocket is unavailable. The process runs until it receives SIGTERM or SIGINT, at which point it shuts down cleanly.

### [internal/shem/worker](modules/internal_shem_worker.md)

The worker module is the execution layer of the Golem shem (agent daemon). It contains a Worker that registers with the orchestrator, resumes in-flight tickets after a restart, polls and reacts to WebSocket pushes to claim available tickets or revision requests under a concurrency limit, cancels and cleans up worktrees for requeued or closed tickets, and dispatches each claimed ticket to a pluggable Executor. The concrete GolemExecutor drives a ticket through brainstorm → plan → implement (plus a revising path for post-review feedback), scaffolding the local ticket via the `golem` CLI, ensuring the repo is initialised and the code graph current, running one `claude --print` session per phase with a purpose-built prompt, posting status/document/approval entries and checkpoints to the orchestrator, blocking on human approval gates between phases, and re-running a phase when a human requests changes. A tail goroutine forwards new log.jsonl lines to the orchestrator in real time. RecoverTicket reconstructs local state (clone, worktree, hard reset to the checkpoint SHA, state.json and log.jsonl) from a ClaimResponse so a restarted shem can resume mid-ticket, and PostCheckpointWithRetry wraps client checkpoint posting with a temporary retry-attempt override.

## orchestrator

### [internal/orchestrator/urlnorm](modules/internal_orchestrator_urlnorm.md)

The urlnorm module provides a single canonicalization function for git remote URLs. It lowercases the scheme and host, strips trailing ".git" suffixes and trailing slashes, and removes query strings and fragments so that two URLs pointing to the same repository always compare equal. The module exists to give the orchestrator a reliable identity key for remote repositories regardless of how the URL was originally typed or cloned.

### [internal/orchestrator/ws](modules/internal_orchestrator_ws.md)

This module implements the WebSocket connection layer for the Golem orchestrator, providing a thread-safe Hub that tracks active shem connections keyed by shem ID, supports targeted Push and repo-scoped Broadcast message delivery, and runs a background heartbeat monitor that detects dead shems (those that have not sent a heartbeat within a configurable timeout), marks them offline in the database, and returns their in-progress tickets to the unassigned pool.

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

This module is the persistence layer for the Golem orchestrator server. It exposes a single Open function that picks a PostgreSQL or pure-Go SQLite driver based on the DSN prefix, enables SQLite WAL journal mode so the server and admin CLI can share one database file concurrently, runs GORM AutoMigrate over every model, and then bootstraps an admin: if no user carries the "admin" role it promotes the lowest-ID existing user, which keeps a pre-RBAC deployment (whose rows AutoMigrate backfills with the default "developer" role) from locking itself out of user management and also recovers a database whose last admin was deleted out-of-band. The role name is written as a string literal rather than imported because the rbac package depends on db, not the reverse. The models it defines — User (with an rbac role name), Session, Shem, Ticket, LogEntry, and HumanInput — represent the full persistent state of the orchestrator: web UI users and their sessions, registered Shem workers and the repos they manage, work tickets with their lifecycle phase, checkpoint and creating user, structured log entries produced by Shems, and human-input requests that block ticket progress pending an operator response. It also provides small query helpers on those models, including a batched creator-name lookup for ticket lists and JSON decoding of a Shem's repo list.

## testing

### [internal/e2e](modules/internal_e2e.md)

End-to-end test suite that validates the full Golem ticket lifecycle from three angles: a unit-level integration test exercising the Go API directly with a mock agent backend, a binary-level test that builds the real golem executable and runs CLI commands against a temporary git repository, and an orchestrator test that exercises concurrent ticket claiming, heartbeat-based shem recovery, and SSE log forwarding using an in-memory SQLite database. The module exists to catch regressions that only surface when all subsystems are wired together, covering workspace creation, ticket persistence, observer dispatch, blog log correctness, and orchestrator coordination primitives.

## ticket

### [internal/slug](modules/internal_slug.md)

The slug module converts free-text ticket titles into filesystem- and git-safe strings for use in branch names. It provides a Slug function that lowercases input, collapses non-alphanumeric runs into single hyphens, trims edge hyphens, truncates to 40 characters, and falls back to "untitled" for empty results, plus a Branch function that composes a full ticket branch name from a title and ticket ID using the pattern ticket/<slug>-<id[:8]>.

### [internal/ticket](modules/internal_ticket.md)

The ticket module defines the lifecycle state for a Golem development ticket. It models the discrete phases a ticket moves through (brainstorm, plan, approve, implement, review, ready-for-review, needs-attention, close, closed), holds associated metadata such as branch name and worktree path, and provides JSON-backed persistence via Save and Load. New tickets start at PhaseBrainstorm unless marked trivial, in which case they skip directly to PhasePlan.

## tickets

### [internal/bloat](modules/internal_bloat.md)

The bloat package provides a deterministic scope-bloat check for commit diffs. It compares the actual number of changed lines against a plan step's stated expectation and flags the diff as SCOPE_BLOAT when the actual count exceeds a fixed multiplier threshold. When no expected line count is provided, the check abstains, deferring scope judgment to LLM roles.

## ui

### [internal/orchestrator/ui](modules/internal_orchestrator_ui.md)

This module provides the HTTP handler layer and embedded HTML template engine for the Golem Orchestrator web dashboard. It parses all page and partial templates from an embedded FS into a page-keyed map at startup (layout + page + partials, with helper template funcs for truncation and display titles) and exposes handlers for login/logout, the ticket dashboard, shem listing, ticket creation, ticket detail, and admin user management. Every authenticated route is registered through a single sessionRoute helper that composes session authentication with an RBAC permission check, so the route table doubles as the authorization audit list; the shared base render map carries the current user's identity plus permission flags so the layout can hide controls the user may not use. The ticket detail view separates SPEC and PLAN documents from the regular log stream and surfaces any pending human-input request, the user-management handlers delegate to the admin package and translate its last-admin errors (plus a self-delete refusal) into re-rendered form validation messages, and an SSE-compatible renderer converts structured log events into HTML fragments for live streaming to the browser.

## wiki

### [internal/wiki](modules/internal_wiki.md)

This module implements a TF-IDF vector search index over wiki documents (markdown files), providing semantic-quality document retrieval without external API dependencies or credentials. It handles the full lifecycle: loading markdown files from disk with SHA-256 content hashing via LoadAll and Hash, building and normalizing TF-IDF vectors with co-mention link edge extraction, searching with optional one-hop link expansion via SearchExpanded, and persisting/restoring the index to disk via gob encoding. Staleness detection in EnsureIndex ensures the index is rebuilt whenever wiki content changes, making it the single entry point for callers that need an always-fresh index.

## workspace

### [internal/workspace](modules/internal_workspace.md)

The workspace module manages Git worktrees for Golem tickets, providing an isolated filesystem checkout and branch per ticket so agents can work concurrently without interfering with the main repository state. It shells out directly to the git CLI to create a worktree at a deterministic path under .golem/tickets/<ticketID>/worktree keyed to a branch and base SHA, and to remove that worktree and its branch once the ticket is done, tolerating cases where the worktree or branch has already been removed.

