<div align="center">
  <img src="docs/hero.svg" alt="Golem" width="100%"/>
</div>

<div align="center">

[![Go](https://img.shields.io/badge/Go-1.27+-00ADD8?style=flat&logo=go)](https://go.dev)
[![License](https://img.shields.io/badge/license-MIT-purple?style=flat)](LICENSE)
[![Backend](https://img.shields.io/badge/backend-agnostic-06b6d4?style=flat)](#backends)

</div>

---

Golem is a workflow orchestrator for AI coding agents. It gives your LLM the same process a good engineering team uses: a ticket lifecycle, distinct roles that check each other's work, a searchable wiki that grows with the project, and a code graph so agents navigate without reading every file. It runs locally, commits to your repo, and works with any LLM backend.

## Why Golem

LLM agents left to their own devices are fast but sloppy. They skip specs, invent abstractions, miss conventions, and forget everything between sessions. Golem doesn't try to make the model smarter — it gives it structure.

Every change starts as a ticket. The ticket goes through **brainstorm → plan → implement → review → close**. At each step, a different role runs: a developer to write code, a convention-enforcer to check patterns, a spec-adherence checker to keep scope honest, a reviewer to attest the whole thing. When a human diverges from what a role recommended, Golem stores it as a **soul entry** — a durable preference that shapes future tickets.

The **code graph** (`golem graph build`) indexes every module so agents find the right file without scanning everything. On a 50-file repo it cuts orientation token usage by ~50%; on larger repos it's closer to 80%.

## Install

**One-liner (Linux / macOS):**

```sh
curl -fsSL https://raw.githubusercontent.com/leonpham/golem/master/install.sh | sh
```

**Go install:**

```sh
go install github.com/leonpham/golem/cmd/golem@latest
```

**Verify:**

```sh
golem help
```

## Quick Start

```sh
# 1. Initialise Golem in your repo
golem init --backend claude-code

# 2. Build the code graph (optional but recommended)
golem graph build

# 3. Open a ticket
golem ticket new --id my-feature "Add rate limiting to the auth middleware"

# 4. Work through the lifecycle
golem ticket advance --ticket my-feature --to plan
golem ticket advance --ticket my-feature --to implement
golem ticket review  --ticket my-feature
golem ticket close   --ticket my-feature
```

## Using with Claude Code

`golem init --backend claude-code` generates two sets of artifacts in addition to the standard `.golem/` config:

**Subagent definitions** (`.claude/agents/`)  
Each role gets a `golem-<role>.md` file. Claude Code loads these as named subagents, so when Golem dispatches a role it runs as a focused one-shot agent rather than a free-form session.

**Slash commands** (`.claude/commands/golem/`)  
Two commands are installed automatically:

- `/golem:new-ticket` — walks the full ticket lifecycle interactively from inside a Claude Code session: brainstorm, plan, implement with observer checkpoints, review, close.
- `/golem:tickets` — lists active tickets and their current phase.

**How dispatch works**  
Each `golem observer dispatch` call shells out to `claude --print` with the role prompt and ticket context piped via stdin. It's a one-shot call — no persistent session, no conversation history. The output is parsed and written to the ticket's blackboard log.

**Per-worktree permissions**  
When Golem creates a worktree for a ticket, it writes a `.claude/settings.json` scoped to that worktree. This grants the agent only the permissions it needs (`git`, `golem`, build/test commands) and nothing else.

**Role files are the source of truth**  
`.golem/roles/` contains the canonical role prompts. The Claude Code subagent files are generated projections — if you edit a role file and want the subagent updated, re-run `golem init`.

## How It Works

### Tickets

Every piece of work is a ticket. Golem creates a dedicated git worktree and branch (`ticket/<id>`) so work is isolated from main and from other in-flight tickets.

```
.golem/tickets/<id>/
  state.json   ← phase, branch, worktree path
  log.jsonl    ← append-only blackboard: every finding, blocker, resolution
```

### Roles

Four roles ship by default. Each is a markdown prompt in `.golem/roles/` — owned by your repo and editable after `init`. To customise a role, edit its `.golem/roles/<role>.md` file directly.

| Role | When it runs | What it checks |
|---|---|---|
| `developer` | During implement | Writes code following YAGNI/KISS identity |
| `convention-enforcer` | After every commit | Naming, structure, project patterns |
| `spec-adherence` | After every commit | Scope creep, plan alignment |
| `reviewer` | At `ticket review` | Full attestation across all categories |

### Observer

The observer is how Golem runs watcher roles after each commit. You call it manually — this keeps you in control of when checks fire:

```sh
golem observer dispatch --ticket <id> --role convention-enforcer --commit $(git rev-parse HEAD)
golem observer dispatch --ticket <id> --role spec-adherence     --commit $(git rev-parse HEAD)
```

Each dispatch is fire-and-forget: the role runs as a one-shot agent, reads the diff and ticket log, and writes its findings back to the blackboard. Multiple dispatches can run in parallel. Check for unresolved blockers before continuing:

```sh
golem ticket resume --id <id>
```

### Gate

Before `ticket review` is allowed, Golem runs the commands in your `gate.commands` config. If any command fails, review is blocked. This ensures the build is green and tests pass before a reviewer role runs.

```yaml
gate:
  commands:
    - go build ./...
    - go test ./...
```

### Wiki

`.golem/wiki/` is a markdown knowledge base committed to your repo. Agents search it before writing code to avoid reinventing what already exists:

- `modules/` — one file per module, updated as code evolves
- `soul/` — learned preferences extracted from human divergences at ticket close
- `graph/` — the code graph index

Search is TF-IDF with co-mention link expansion, zero API calls:

```sh
golem wiki search "rate limiting"
```

### Code Graph

```sh
golem graph build              # full index of the entire repo
golem graph update             # incremental — only changed modules since last build
golem graph status             # show what's stale
```

The graph-builder agent analyses each module and emits structured output:

```
GRAPH_MODULE:src/auth
GRAPH_SUMMARY:Handles JWT validation, session creation, and RBAC.
GRAPH_EXPORT_FN:validate_token(token: str) -> Claims — validates JWT
GRAPH_EXPORT_TYPE:Claims — JWT payload: user_id, role, expires_at
GRAPH_IMPORTS:src/models,src/config
GRAPH_SUBSYSTEM:auth
```

Output is stored in `.golem/wiki/graph/` and automatically included in wiki search. The graph is language-agnostic — Go, Python, TypeScript, Rust, or anything your backend can read.

### Soul

When a human resolves a ticket differently from what a role recommended, Golem notices. At `ticket close`, the reviewer generalises the divergence into a durable heuristic and promotes it to `.golem/wiki/soul/`. Future agents find it via wiki search and apply it automatically.

## Backends

Golem's orchestration core never calls a backend directly — all execution goes through the `Runner` interface (`RunAgent` + `WorktreeSetup`). Adding a new backend means implementing those two methods and registering the backend name in config.

| Backend | Status | Config |
|---|---|---|
| `claude-code` | Supported | `golem init --backend claude-code` |
| `gemini`, `codex`, others | Planned | Implement the `Runner` interface |

## Configuration

`.golem/config.yaml` is created by `golem init` and committed to your repo:

```yaml
backend: claude-code

gate:
  commands:             # run before marking a ticket ready-for-review
    - go build ./...
    - go test ./...

tool_policy:
  allow_network: []     # hosts agents may curl/wget
  allow_worktree_only: true

graph:
  ignore_patterns:      # additional paths to skip during graph build
    - "migrations/**"
  max_file_size_kb: 200
  extra_extensions:
    - ".graphql"
    - ".proto"

role_models:            # per-role model overrides
  reviewer: claude-opus-4-7
```

## Commands

```
golem help                                     show all commands
golem help <command>                           show usage for a command

golem init --backend <name>                    initialise .golem in a repo
golem init --backend <name> --with-graph       init and build the code graph

golem ticket new --id <id> "<description>"     create ticket, worktree, branch
golem ticket advance --ticket <id> --to <phase>
golem ticket set-step --ticket <id> --expected-lines <n>
golem ticket review  --ticket <id>
golem ticket close   --ticket <id>
golem ticket resume  --id <id>
golem tickets                                  list all active tickets

golem graph build   [--concurrency N]          full graph rebuild
golem graph update  [--concurrency N]          incremental update
golem graph status                             show stale modules

golem wiki search "<query>"                    TF-IDF search over wiki + soul
golem wiki rebuild                             force index rebuild

golem observer dispatch --ticket <id> --role <role> --commit <sha>
golem log emit --ticket <id> --role <role> --type <type> <message>
golem ask    --ticket <id> --from <role> --to <role> <question>
golem answer --ticket <id> --from <role> --in-reply-to <id> <answer>
```

## Developing Golem

### Prerequisites

- Go 1.27+
- Git

### Build

```sh
git clone https://github.com/leonpham/golem
cd golem
go build ./cmd/golem/
go test ./...
```

### Project Layout

```
cmd/golem/          ← CLI entry point and command dispatch
internal/
  agentrunner/      ← Runner interface + backend adapters
  cli/              ← command handlers (one file per command)
  config/           ← config.yaml parsing
  graph/            ← code graph: discovery, parser, writer, meta
  wiki/             ← TF-IDF index + document loader
  ticket/           ← ticket state machine
  blog/             ← append-only blackboard log
  observer/         ← signal claiming and role dispatch
  workspace/        ← git worktree creation and teardown
  roles/            ← embedded default role prompts
  soul/             ← learned preference extraction and promotion
  gate/             ← deterministic build/test gate runner
```

Golem is developed on itself — new features ship as tickets on the same workflow described above.

---

<div align="center">

`golem build .. golem`

</div>
