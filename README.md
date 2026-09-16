<div align="center">
  <img src="docs/hero.svg" alt="Golem" width="100%"/>
</div>

<div align="center">

[![Go](https://img.shields.io/badge/Go-1.27+-00ADD8?style=flat&logo=go)](https://go.dev)
[![Backend](https://img.shields.io/badge/backend-agnostic-06b6d4?style=flat)](#backends)

</div>

---

Golem is a workflow layer for AI coding agents. It gives your LLM the same process a good engineering team uses: a ticket lifecycle, distinct roles that check each other's work, a searchable wiki that grows with the project, and a code graph so agents navigate without reading every file.

Two modes, one tool:

- **CLI** — run Golem locally, one developer, one repo. Full lifecycle from your terminal.
- **Orchestrator + Shem** — distributed mode. A central web UI manages tickets; autonomous worker nodes (shems) claim and execute them in parallel, streaming progress back to the dashboard.

## Why Golem

LLM agents left to their own devices are fast but sloppy. They skip specs, invent abstractions, miss conventions, and forget everything between sessions. Golem doesn't try to make the model smarter — it gives it structure.

Every change starts as a ticket. The ticket goes through **brainstorm → plan → implement → review → close**. At each step, a different role runs: a developer to write code, a convention-enforcer to check patterns, a spec-adherence checker to keep scope honest, a reviewer to attest the whole thing. Humans approve transitions between phases, staying in control without doing the work.

The **code graph** (`golem graph build`) indexes every module so agents find the right file without scanning everything. On a 50-file repo it cuts orientation token usage by ~50%; on larger repos it's closer to 80%.

---

## CLI Mode

### Install

```sh
go install github.com/leonp92/golem/cmd/golem@latest
```

### Quick Start

```sh
# 1. Initialise Golem in your repo
golem init --backend claude-code

# 2. Build the code graph (optional but recommended for larger repos)
golem graph build

# 3. Open a ticket
golem ticket new "Add rate limiting to the auth middleware"

# 4. Work through the lifecycle
golem ticket advance --to plan
golem ticket advance --to implement
golem ticket review
golem ticket close
```

### How It Works

#### Tickets

Every piece of work is a ticket. Golem creates a dedicated git worktree and branch (`ticket/<id>`) so work is isolated from main and from other in-flight tickets.

```
.golem/tickets/<id>/
  state.json   ← phase, branch, worktree path
  log.jsonl    ← append-only blackboard: every finding, blocker, resolution
```

#### Roles

Four roles ship by default. Each is a markdown prompt in `.golem/roles/` — owned by your repo and editable after `init`.

| Role | When it runs | What it checks |
|---|---|---|
| `developer` | During implement | Writes code following YAGNI/KISS identity |
| `convention-enforcer` | After every commit | Naming, structure, project patterns |
| `spec-adherence` | After every commit | Scope creep, plan alignment |
| `reviewer` | At `ticket review` | Full attestation across all categories |

#### Observer

The observer dispatches watcher roles after commits. You call it manually, keeping control of when checks fire:

```sh
golem observer dispatch --ticket <id> --role convention-enforcer --commit $(git rev-parse HEAD)
golem observer dispatch --ticket <id> --role spec-adherence     --commit $(git rev-parse HEAD)
```

Each dispatch is fire-and-forget: the role reads the diff and ticket log, writes findings to the blackboard. Multiple dispatches run in parallel.

#### Gate

Before `ticket review` is allowed, Golem runs the commands in your `gate.commands` config. If any command fails, review is blocked.

```yaml
gate:
  commands:
    - make build
    - make test
```

#### Code Graph

```sh
golem graph build              # full index of the entire repo
golem graph update             # incremental — only changed modules since last build
golem graph status             # show what's stale
```

The graph-builder agent analyses each module and writes structured summaries — exports, types, imports, subsystem — into `.golem/wiki/graph/`, included in wiki search. Language-agnostic.

#### Wiki

`.golem/wiki/` is a markdown knowledge base committed to your repo. Agents search it before writing code to avoid reinventing what already exists. Search is TF-IDF with co-mention link expansion — zero API calls:

```sh
golem wiki search "rate limiting"
```

#### Soul

When a human resolves a ticket differently from what a role recommended, Golem notices. At `ticket close`, the reviewer generalises the divergence into a durable heuristic and promotes it to `.golem/wiki/soul/`. Future agents find it via wiki search and apply it automatically.

#### GitHub Issues

`golem issue list` and `golem issue sync` pull issues from a GitHub repo onto local tickets — both are read-only against GitHub, they never create, comment on, or close anything:

```sh
export GOLEM_GITHUB_TOKEN=ghp_...          # required for all `golem issue` commands

golem issue list                           # open issues carrying the trigger label
golem issue sync --ticket <id>             # refresh a ticket's description (title + body) from its linked issue

golem ticket new --from-issue 42 --id t1   # create a ticket from issue #42; the description is the issue's title, a blank line, then its body
```

Configure the repo and trigger label in `.golem/config.yaml`:

```yaml
github:
  repo: your-org/your-repo   # "org/repo"; required, not inferred from the git remote
  label: golem                # trigger label for `golem issue list`; defaults to "golem"
  write: true                 # see "Two-writer guard" below
```

**Two-writer guard:** `github.write` controls whether this repo is allowed to have the CLI act as a GitHub writer. `golem init` sets it to `true` for standalone use. When a shem (see Orchestrator + Shem below) initialises or preflights a repo it manages, it forces `github.write` back to `false` on every run — an orchestrator-managed repo must have exactly one writer, or the CLI and the orchestrator would post duplicate comments and fight over labels. `golem issue list`, `golem issue sync`, and `golem ticket new --from-issue` do not themselves call any GitHub write endpoint, so `github.write` currently has no enforcement effect on the CLI — it only records which side owns write access. A future write-capable CLI command must check `cfg.GitHub.Write` itself before writing; nothing does that automatically today.

### Using with Claude Code

`golem init --backend claude-code` generates subagent definitions in `.claude/agents/` and two slash commands:

- `/golem:new-ticket` — walks the full ticket lifecycle interactively from inside Claude Code.
- `/golem:tickets` — lists active tickets and their current phase.

### Configuration

`.golem/config.yaml` is created by `golem init` and committed to your repo:

```yaml
backend: claude-code

gate:
  commands:
    - make build
    - make test

graph:
  ignore_patterns:
    - "migrations/**"
  max_file_size_kb: 200
  extra_extensions:
    - ".graphql"
    - ".proto"

role_models:
  reviewer: claude-opus-4-7

github:
  repo: your-org/your-repo
  label: golem
  write: true
```

### Commands

```
golem init --backend <name>                    initialise .golem in a repo

golem ticket new "<description>"               create ticket, worktree, branch
golem ticket advance --to <phase>
golem ticket set-step --expected-lines <n>
golem ticket review
golem ticket close
golem ticket resume
golem tickets                                  list all active tickets

golem graph build   [--concurrency N]          full graph rebuild
golem graph update  [--concurrency N]          incremental update
golem graph status                             show stale modules

golem wiki search "<query>"
golem wiki rebuild

golem issue list                               list open issues carrying the trigger label
golem issue sync --ticket <id>                 refresh a ticket's description from its linked issue
golem ticket new --from-issue <n>              create a ticket from a GitHub issue's title and body

golem observer dispatch --ticket <id> --role <role> --commit <sha>
golem log emit --ticket <id> --role <role> --type <type> <message>
```

---

## Orchestrator + Shem (Distributed Mode)

The orchestrator adds a team-scale coordination layer on top of the CLI. Instead of running tickets manually, you create them in a web UI and autonomous **shem** worker nodes execute them in parallel.

```
Browser ──► Orchestrator (dashboard, approval gates)
                │
      ┌─────────┴──────────┬──────────┐
   Shem-A               Shem-B     Shem-C
  (worker)             (worker)   (worker)
   repo A               repo A     repo B
```

**Benefits over CLI mode:**

- **Parallel execution** — multiple tickets run simultaneously across one or more shems. Work doesn't queue behind a single terminal session.
- **Human approval gates** — brainstorm and plan phases pause for review in the UI before the shem continues. You see the spec and plan before any code is written.
- **Real-time visibility** — the blackboard log streams to the dashboard as the shem works. Blockers and findings surface immediately.
- **Atomic ticket claiming** — multiple shems on the same repo claim tickets without races. No duplicated work.
- **Horizontal scaling** — add more shem nodes to increase parallelism. Each shem is stateless; the orchestrator holds all state in SQLite.

### Quick Start (Docker Compose)

```sh
# Clone the repo, then:
./run.sh
```

On first run the script creates `.env` from the example and exits — fill in `GOLEM_ADMIN_PASSWORD` and one Claude Code auth option (`CLAUDE_HOME`, `CLAUDE_CODE_OAUTH_TOKEN`, or `ANTHROPIC_API_KEY`), then run it again. It auto-generates a shem API key and starts the stack. Open `http://localhost:8080` when it's done.

`GOLEM_GITHUB_TOKEN` in the same file is optional and turns on the GitHub Issues integration — see [GitHub Issues](#github-issues-1) below for what else it needs.

Create a ticket from the UI. The shem will pick it up within seconds, run brainstorm, and pause for your approval before proceeding to plan and implementation.

### How It Works

1. You create a ticket in the UI with a description and target repo.
2. A shem claims it atomically via the REST API.
3. The shem runs `golem ticket new` to scaffold the worktree, then invokes Claude Code for the brainstorm phase.
4. The shem posts the spec to the orchestrator and waits for human approval.
5. On approval, the shem runs the plan phase — same gate, same wait.
6. On approval, the shem implements and reviews. When done, the ticket enters `ready-for-review`.
7. You close the ticket from the UI.

At every step the shem tails the ticket's `log.jsonl` and forwards entries to the orchestrator over HTTP, which fans them out to the browser via SSE.

### GitHub Issues

Issues carrying a trigger label become tickets. The orchestrator polls, ingests, and — as the ticket moves — labels the issue, comments on it, closes it, and opens the pull request.

A ticket's description is the issue's **title and body**: the title, a blank line, then the body, or the title alone when the body is empty. That is byte-for-byte what `golem ticket new --from-issue` stores in CLI mode, so an issue produces the same ticket either way, and an issue whose title says everything still gives the agent something to work on.

**There is no add-repo form and no token field on `/settings/github`.** Repositories are discovered from the shems that register with them, and the token is read from the environment only, never stored in the database. The full path from a fresh install to a working sync is:

1. **Give the orchestrator a token.** Create a fine-grained PAT with, on the target repository: Issues read/write, Pull requests read/write, Contents read, Metadata read. Put it in `.env` as `GOLEM_GITHUB_TOKEN` or in the orchestrator's environment directly. The variable's name is configurable as `github.token_env` in `orchestrator.yaml`. Docker compose gives this one to the orchestrator only — the shem's push credential is a separate, opt-in variable, for the reason given under [Shem Configuration](#shem-configuration).

   The token is read once, at startup. If it was empty when the orchestrator started, sync stays off until you set it and **restart** — the log says so, and **Sync now** answers "GitHub sync is not running on this orchestrator". Enabling a repository does not need a restart; only supplying the token for the first time does.

2. **Set `base_url` in `orchestrator.yaml`** to the orchestrator's externally reachable address, e.g. `https://golem.example.com`. Golem's milestone comments link back to `<base_url>/tickets/<id>`; leaving it empty produces a bare `/tickets/<id>` with no host — a broken link in a real GitHub comment. Startup logs a WARNING when sync runs without it.

3. **Point a shem at the repository.** Add it to the shem's `repos:` list with its GitHub remote:

   ```yaml
   repos:
     - path: /repos/your-repo
       remote: https://github.com/your-org/your-repo
   ```

   The repository appears on `/settings/github` once that shem has registered.

4. **Enable it on `/settings/github`.** Tick **Enabled** and set the trigger label (default `golem`; it must not start with `golem:`, which is Golem's own namespace — see below). Polling starts on the next interval, or immediately via **Sync now**.

5. **Approve each ingested ticket.** See the next section — this step is required and is easy to miss.

Poll interval, drain interval, manual-sync cooldown, and a GitHub Enterprise `api_base` are all under `github:` in `orchestrator.yaml`.

#### The intake approval gate

**An ingested ticket is not claimable until a human approves it.** It arrives in phase `pending-approval`, and no shem can take it until someone opens it in the dashboard, reads the description, and presses **Approve & Start**.

This is deliberate and is the feature's load-bearing control. An issue is written by anyone who can open one on that repository, and its text is interpolated into the prompts that drive brainstorm, plan, implement, and revise — one of which has shell and repository write access.

The approval binds to the exact text you were shown, **title included**. If the issue is edited between the page rendering and your click, the approval is refused with a 409 and you are asked to reload and read the new text. If the issue is edited *after* approval and the ticket has not been claimed yet, it returns to `pending-approval` for a fresh read; if it has already been claimed, the running agent keeps working from the text that was approved and the change is recorded in the ticket's log. Editing only the title counts as an edit — the title is part of what the agent is given, so it is part of what you approved.

The CLI has no such gate, on purpose: `golem ticket new --from-issue 42` is typed by the human who would otherwise be approving, so the act of running it *is* the approval.

#### The `golem:<phase>` label convention

Golem owns the `golem:` prefix on issues it manages and nothing else. On each phase transition it applies `golem:<phase>` — `golem:pending-approval`, `golem:brainstorm`, `golem:plan`, `golem:implement`, `golem:ready-for-review` — and removes any *other* `golem:*` label, so an issue carries exactly one at a time. Every other label, including the trigger label `golem` (no colon) and all of your own, is left untouched.

That is also why the trigger label may not itself start with `golem:`: the first phase transition would strip it and un-enroll the issue from its own filter. The settings form rejects such a label.

Golem also comments at three milestones — spec written, plan approved, implementation complete — closes the issue when the ticket is closed, and opens a draft pull request once the ticket reaches `ready-for-review` and its branch has been pushed. Pushing requires `no_push: false` in the shem config plus a push credential; see `deploy/shem.yaml`.

When a GitHub write fails often enough, it parks and stops retrying. Parked writes are listed at the bottom of `/settings/github` with a **Retry** button — check there if the issue stops reflecting the ticket.

The orchestrator is the sole GitHub writer for any repo it manages: it posts comments, updates labels, and reflects ticket-state changes back onto the linked issue. This is why the shem forces `github.write: false` in that repo's `.golem/config.yaml` (see "Two-writer guard" under CLI Mode above) — the CLI must stay read-only there so the two systems never race each other on the same issue.

### Upgrading

Upgrading an existing deployment to the GitHub Issues release has one required
step for some databases and three behaviour changes worth knowing about. See
[docs/releases/2026-09-github-issues-integration.md](docs/releases/2026-09-github-issues-integration.md).

**Run this before you start the new binary**, not after. If your database was
last written by a build from partway through this work, some of its
GitHub-linked tickets carry a blank approval hash, and the release note
explains which of those shapes were claimable without one:

```sh
golem-orchestrator backfill body-hash --dry-run   # rehearse; writes nothing
golem-orchestrator backfill body-hash
```

### Shem Configuration

```yaml
# shem.yaml
orchestrator: http://localhost:8080
name: my-shem
# api_key: set via GOLEM_SHEM_API_KEY env var

no_push: true   # for local testing; see below

repos:
  - path: /path/to/local/repo
    remote: https://github.com/your-org/your-repo
```

`no_push: true` routes `git push` to the local repository instead of a remote, which is ideal for trying Golem out — but it also means **no branch reaches GitHub and no pull request is ever opened**. The shipped `deploy/shem.yaml` keeps it on, because the compose stack's default repositories are a throwaway local one and whatever `GOLEM_REPO_PATH` points at. To get the pull request, set `no_push: false` and give the shem a push credential: `GOLEM_SHEM_GITHUB_TOKEN` in `.env` (the container's entrypoint installs it as an HTTPS credential helper for github.com) or an SSH key. Without one the push fails and the pull request is silently never opened — the ticket still reaches `ready-for-review`.

That is a different variable from the orchestrator's `GOLEM_GITHUB_TOKEN`, and it is empty unless you set it. The shem container is where the agent runs, and it runs on issue text anyone can open an issue to write; the approval gate makes a successful prompt injection unlikely, not impossible. So the shem is handed a repository credential only when someone has decided it needs one. Use a second, push-only token rather than the orchestrator's if you can. The agent subprocess itself never sees either token — Golem runs `claude` with an allow-listed environment that excludes everything in the `GOLEM_` namespace, so the credential helper works for Golem's own `git push` and yields nothing to the agent.

### Running Multiple Shems

Each shem is a stateless binary. To scale:

```sh
# On additional machines or containers, point at the same orchestrator
GOLEM_SHEM_API_KEY=<key> golem-shem --config shem.yaml
```

Register each shem with a unique name:

```sh
golem-orchestrator shems add --name shem-2
```

### Running Without Docker

```sh
# Orchestrator
go build -o golem-orchestrator ./cmd/orchestrator
./golem-orchestrator orchestrator.yaml

# Shem (same or different machine)
go build -o golem-shem ./cmd/shem
./golem-shem shem.yaml
```

---

## Backends

Golem's core never calls a backend directly — all execution goes through the `Runner` interface (`RunAgent` + `WorktreeSetup`). Adding a new backend means implementing those two methods.

| Backend | Status |
|---|---|
| `claude-code` | Supported |
| `gemini`, `codex`, others | Planned |

---

## Project Layout

```
cmd/
  golem/            ← CLI entry point
  orchestrator/     ← web server + admin subcommands
  shem/             ← distributed worker binary
internal/
  agentrunner/      ← Runner interface + backend adapters
  cli/              ← command handlers
  graph/            ← code graph: discovery, parser, writer, meta
  wiki/             ← TF-IDF index + document loader
  ticket/           ← ticket state machine
  blog/             ← append-only blackboard log
  observer/         ← role dispatch
  workspace/        ← git worktree management
  roles/            ← embedded default role prompts
  soul/             ← learned preference extraction
  gate/             ← build/test gate runner
  orchestrator/     ← orchestrator packages (db, auth, api, ws, sse, ui)
  shem/             ← shem packages (config, client, worker)
deploy/
  orchestrator.yaml ← orchestrator config for Docker Compose
  shem.yaml         ← shem config for Docker Compose
  compose_test.go   ← keeps docker-compose.yml honest about what it delivers
docs/
  releases/         ← upgrade notes
```

Golem built Golem. The reviewer approved.

## License

MIT — see [LICENSE](LICENSE).
