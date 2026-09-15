# GitHub Issues Integration — Design

- **Date:** 2026-09-15
- **Status:** Approved, ready for implementation planning
- **Scope:** Bidirectional sync between GitHub Issues and Golem tickets, across both orchestrator and CLI modes.

## Goal

Make a GitHub issue the entry point to Golem's ticket lifecycle, and make the
issue reflect what Golem is doing with it. A team labels an issue; a shem picks
it up, runs brainstorm → plan → implement → review, and the issue shows the
phase, the milestones, the pull request, and finally closes.

## Decisions

These were settled during brainstorming and are not open for re-litigation
during implementation. Anything not listed here is an implementation choice.

| Decision | Value |
|---|---|
| Sync direction | Full bidirectional |
| Sync engine location | Orchestrator, with CLI parity commands |
| GitHub transport | `github.com/google/go-github` + fine-grained PAT from env |
| Inbound mechanism | Polling (no webhooks) |
| Ingest interval | 15 minutes, plus an enforced manual trigger |
| Outbox drain interval | 20 seconds |
| Issue selection | Opt-in label (default `golem`) |
| Work start | Automatic — labeled issues enter as `unassigned` and shems claim them |
| Write-back | Phase labels, milestone comments, PR at ready-for-review, issue close |
| Conflict resolution | GitHub is always the source of truth for content fields |

### Consequences of GitHub-as-source-of-truth

Because GitHub always wins on content, `Title` and `Description` render
**read-only** in the Golem UI for GitHub-linked tickets, annotated
"synced from GitHub #N" with a link to edit on GitHub. Leaving them editable
would let a user's edit silently revert within one ingest cycle, which reads as
a bug rather than as policy.

Golem-owned fields have no GitHub counterpart and are never overwritten by
ingest: `Phase`, `Branch`, `BaseBranch`, checkpoints, spec, plan, and the
`golem:*` label namespace.

## Non-goals

- Webhooks. Polling was chosen deliberately so the default
  `http://localhost:8080` install needs no public reachability and no per-repo
  webhook setup.
- GitHub App identity. A PAT is the chosen auth; a Golem GitHub App is a
  possible future change, not part of this work.
- Syncing unlabeled issues. Repos with large backlogs must not flood the
  dashboard, and Golem must not write to issues nobody opted in.
- Conflict-resolution UI. The source-of-truth rule removes the need for one.
- Relaxing agent tool gating. See "Gating is unaffected" below.

## Gating is unaffected

`internal/gating/policy.go:17` denies `gh pr create` and `gh pr comment`, and
`internal/gating/policy.go:16` denies `git push`. These stay as they are.

`gating.Evaluate` is the enforcement point for **agent** tool calls, invoked
from the backend adapter's PreToolUse-equivalent hook. Every GitHub write in
this design is made by Golem's own Go code through the `internal/github`
client, and every branch push is made by the shem executor directly. Neither
path passes through `gating.Evaluate`. Agents remain denied; Golem the system
performs these actions.

## Architecture

```
                       ┌──────────────────────────────────┐
  GitHub Issues  ◄─────┤  orchestrator: ghsync worker     │
                       │                                  │
    poll 15m ─────────►│  ingest.go      (issues→tickets) │
    manual trigger ───►│  reconcile.go   (label drift)    │
                       │                                  │
    writes    ◄────────┤  publish.go     (outbox drain)   │◄── outbox rows
                       └──────────────────────────────────┘    written in the
                                      │                        same txn as the
                                      ▼                        ticket change
                              SQLite / Postgres
                                      ▲
                                      │ claim, phase updates
                                 shem worker
```

### Package layout

```
internal/github/                  ← typed client over go-github
  client.go      Client interface + real implementation
  issues.go      list / get / create / set state
  labels.go      add / remove / set
  comments.go    create comment
  pulls.go       create pull request
  fake.go        in-memory Client for tests

internal/orchestrator/ghsync/
  worker.go      two tickers (ingest, drain) + manual trigger channels
  ingest.go      GitHub → tickets
  publish.go     outbox drain
  reconcile.go   diffable state correction
  outbox.go      transactional Enqueue
```

`ghsync` depends only on the `github.Client` interface, so the entire sync
engine is testable against `github.Fake` with no network. File sizes follow the
project convention of small, focused files (target 200–400 lines, 800 max).

## Data model

### Changes to `db.Ticket` (`internal/orchestrator/db/models.go`)

```go
IssueNumber *int   `json:"issue_number"`
IssueURL    string `json:"issue_url"`
PRNumber    *int   `json:"pr_number"`
PRURL       string `json:"pr_url"`
BranchPushed bool  `gorm:"not null;default:false" json:"branch_pushed"`
```

A **unique composite index on `(repo_remote, issue_number)`** guarantees one
ticket per issue. SQLite and Postgres both treat NULLs as distinct in unique
indexes, so tickets created in the UI (with a NULL `issue_number`) are
unaffected and may coexist freely.

`BranchPushed` gates PR creation; see "Branch push and PR creation".

### New model: `GitHubRepo`

```go
type GitHubRepo struct {
    ID              uint   `gorm:"primaryKey"`
    RepoRemote      string `gorm:"uniqueIndex;not null"` // normalized via urlnorm
    Owner           string `gorm:"not null"`
    Name            string `gorm:"not null"`
    Enabled         bool   `gorm:"not null;default:false"`
    Label           string `gorm:"not null;default:'golem'"`
    LastIssueSync   *time.Time // `since` cursor for ListByRepo
    LastPolledAt    *time.Time
    LastManualSync  *time.Time // cooldown enforcement
    ETag            string
    LastError       string
}
```

`Owner` and `Name` are derived from `RepoRemote` at creation time so the sync
loop never re-parses URLs.

### New model: `GitHubOutbox`

```go
type GitHubOutbox struct {
    ID             uint      `gorm:"primaryKey"`
    TicketID       string    `gorm:"not null;index"`
    Kind           string    `gorm:"not null"` // comment | label | pr | close_issue
    Payload        string    `gorm:"not null"` // JSON, shape depends on Kind
    IdempotencyKey string    `gorm:"uniqueIndex;not null"`
    Attempts       int       `gorm:"not null;default:0"`
    NextAttempt    time.Time `gorm:"index"`
    LastError      string
    DoneAt         *time.Time
}
```

**The idempotency key is the de-duplication mechanism.** It is a deterministic
string derived from the event, for example `<ticketID>:comment:plan-approved`
or `<ticketID>:label:implement`. Enqueuing the same logical event twice — after
a crash, a retry, or a requeued ticket — violates the unique index, and
`Enqueue` swallows that specific violation as success. This is what makes the
publish side safe to re-run without posting duplicate comments.

## Ingest — GitHub to Golem

Runs every 15 minutes, and on manual trigger. For each `GitHubRepo` where
`Enabled` is true:

1. Call `ListIssuesSince(ctx, owner, name, label, LastIssueSync)` with
   `state=all` so closes are observed, sending the stored `ETag` as a
   conditional request. A 304 response skips the repo with no further work.
2. For each returned issue, look up the ticket by `(repo_remote, issue_number)`:

   | Condition | Action |
   |---|---|
   | Not found, issue open, has label | Create ticket: `Title` and `Description` from the issue, `BaseBranch` from the repo default branch, `Branch: slug.Branch(title, id)`, `Phase: "unassigned"`, issue linkage set. A shem claims it via the existing `/api/tickets/available` path — no new claiming logic. |
   | Found | Overwrite `Title` and `Description` from the issue. `Phase` is never touched. |
   | Found, issue closed | Transition the ticket to `closed`. |
      | Not found, issue closed | Ignore. Golem does not resurrect history. |

**Label removal is not observable here.** The list query is label-filtered, so
an issue that loses the label simply stops appearing — it cannot be
distinguished from an issue that was not updated. Removal is therefore detected
by the reconcile pass, which re-fetches each linked issue by number (see
"Reconciliation"). The chosen behaviour is to log it and leave the ticket
running: removing a label is not a cancel signal, and cancelling stays a
deliberate UI action. Until Phase 2 lands, label removal has no effect at all.

3. Advance `LastIssueSync` to `max(updated_at) - 1m` to tolerate clock skew
   between GitHub and the orchestrator, and store the response `ETag`.

The one-minute cursor overlap re-reads a small number of issues each pass. This
is harmless: creates are blocked by the unique index, and updates are
idempotent overwrites.

A poll failure for one repo records `LastError` on that repo and continues to
the next. One misconfigured repo never stalls the others.

## Publish — Golem to GitHub

### Enqueue points

Outbox rows are inserted **inside the same database transaction** as the ticket
change, so a phase can never be committed without its GitHub follow-up queued:

- `Handlers.updatePhase` (`internal/orchestrator/api/tickets.go:296`) — enqueue
  a `label` row for `golem:<phase>` and a `comment` row for the milestone.
- Ticket close, in the UI handler and `internal/orchestrator/api/human.go` —
  enqueue a `close_issue` row.
- The `pr` row is enqueued by **whichever of these two events happens second**:
  the transition to `ready-for-review`, or the `branch-pushed` callback. Each
  checks the other's condition and enqueues only if both now hold. Both paths
  use the same idempotency key `<ticketID>:pr`, so a race between them results
  in exactly one PR.

### Drain

Every 20 seconds, select rows where `done_at IS NULL AND next_attempt <= now()`
ordered by `id`, and execute each against the GitHub client.

- Success marks `DoneAt`.
- Failure increments `Attempts`, records `LastError`, and sets `NextAttempt`
  with exponential backoff: 1m, 2m, 4m, 8m, capped at 30m.
- After 8 attempts the row is **parked** — left undone with no further retries
  — and surfaced in the dashboard as a failed sync. Parked rows are never
  silently dropped.

Because there is exactly one drain loop, it is also the single throttle point
for rate limiting. It honours `X-RateLimit-Remaining` and `Retry-After`, and a
secondary rate limit backs off the whole loop rather than individual rows.

### Milestone comments

One comment per gate, each linking to the ticket in the orchestrator UI:

| Gate | Comment |
|---|---|
| Spec written | "Golem wrote a spec for this issue." + ticket link |
| Plan approved | "Plan approved; implementation starting." + ticket link |
| Implementation complete | "Implementation complete, ready for review." + ticket link + PR link when present |

### Phase labels

Golem owns the `golem:*` label namespace and nothing else. On each phase change
it removes any existing `golem:*` label and applies `golem:<phase>`. Labels
outside that namespace are never touched, so human labels and the opt-in
trigger label survive.

## Branch push and PR creation

Golem does not push branches today. `no_push` and `setPushURL`
(`internal/shem/worker/executor.go:629`) only redirect pushes an agent might
attempt; `recover.go:38` merely tolerates a branch having been pushed. Nothing
in the codebase performs the push.

The orchestrator cannot push — it has no worktree. The shem does. So PR
creation is a two-actor flow:

1. The shem completes the implement phase and the ticket reaches
   `ready-for-review`.
2. The shem runs `git push -u origin ticket/<id>` from the worktree. This is
   **skipped entirely when `no_push: true`**, preserving local-testing
   behaviour.
3. The shem calls `POST /api/tickets/{id}/branch-pushed` (API-key auth, owner
   check identical to `updatePhase`), which sets `BranchPushed` and enqueues
   the `pr` outbox row in one transaction.
4. The drain loop opens a **draft** PR: head `ticket/<id>`, base the ticket's
   `BaseBranch`, title from the ticket title, body containing
   `Closes #<issue>` plus a link to the ticket. `PRNumber` and `PRURL` are
   stored on the ticket.

### Push credentials

The shem needs push rights, which is a **different secret from the
orchestrator's PAT** and must be provisioned on each shem host. Supported:

- `GOLEM_GITHUB_TOKEN` on the shem, used via an HTTPS credential helper
  configured for the repo remote, or
- the operator's existing git credentials (SSH key or credential manager) when
  the shem runs on a developer machine.

If the push fails, the shem records the failure in the ticket log and does not
call `branch-pushed`; no `pr` row is enqueued and the ticket still reaches
`ready-for-review` normally. A missing PR never blocks the lifecycle.

## Reconciliation

The 15-minute ingest pass also reconciles **diffable** state for every open
GitHub-linked ticket in the repo:

- the `golem:<phase>` label matches the ticket's current phase
- the issue's open/closed state matches the ticket's
- the opt-in label is still present; if it was removed, log it and leave the
  ticket running (this is the only place removal is observable — see "Ingest")

Drift from downtime, a parked outbox row, or a hand-edited label self-heals
within one cycle. Comments and PRs are **never** reconciled — they are one-shot
events with no diffable end state, and they belong to the outbox alone.

## Manual sync

**Endpoint:** `POST /api/github/repos/{id}/sync`, behind
`auth.RequireSession` — the human gate, not the shem API-key gate. Shems cannot
trigger syncs.

**Cooldown enforcement:** a per-repo minimum interval
(`github.manual_sync_cooldown`, default 60s) checked against
`GitHubRepo.LastManualSync`. Inside the window the endpoint returns **429** with
the remaining seconds in the body, and the UI renders the button disabled with a
countdown. Without this, a held-down button can exhaust the hourly rate limit
that the 15-minute base interval was chosen to conserve.

**Serialization:** each repo has a buffered-size-1 `chan struct{}`. The trigger
is a non-blocking send; a send onto a full buffer is dropped because a sync is
already queued. The ingest goroutine `select`s across its 15-minute ticker and
that channel, so a manual sync runs the *same* ingest pass as a scheduled one.
One code path, and a manual sync can never run concurrently with a scheduled
pass — preserving the single-writer property the outbox design depends on.

## Configuration

### `orchestrator.yaml`

```yaml
github:
  token_env: GOLEM_GITHUB_TOKEN
  poll_interval: 15m
  drain_interval: 20s
  manual_sync_cooldown: 60s
  api_base: ""          # set for GitHub Enterprise
```

### `.env.example`

```
# Fine-grained PAT for GitHub Issues integration.
# Required repository permissions:
#   Issues:        Read and write
#   Pull requests: Read and write
#   Contents:      Read and write   (branch push for PR creation)
#   Metadata:      Read
GOLEM_GITHUB_TOKEN=
```

### Startup validation

If any `GitHubRepo` has `Enabled` true and the configured token environment
variable is empty, the orchestrator logs a loud error and **disables sync**
rather than silently no-opping. Required secrets are validated at startup, per
the project security rules.

## CLI parity

`.golem/config.yaml` gains:

```yaml
github:
  repo: org/repo    # optional; inferred from git remote origin
  label: golem
  write: false
```

Commands:

- `golem issue list` — open issues carrying the configured label
- `golem ticket new --from-issue <n>` — create a ticket linked to the issue,
  writing `issue_number` and `issue_url` into `state.json`
- `golem issue sync` — pull the latest title and body onto the linked ticket

### The two-writer guard

Orchestrator and CLI are both capable of writing to GitHub, and an orchestrated
repo must have exactly one writer or it will post duplicate comments and fight
over labels.

The guard is `github.write`. `golem init` writes `write: true` for standalone
use. The shem's `ensureRepoReady` (`internal/shem/worker/executor.go`) sets it
to **`false`** for any repo it initializes, because that repo is orchestrator-
managed. When `write` is false the CLI commands are read-only: they pull issue
content onto tickets and never call a GitHub write endpoint.

## UI

- **`/settings/github`** — repos discovered from registered shems; per-repo
  toggle for `Enabled`, label field, last-polled time, last error, and a
  "Sync now" button disabled during cooldown.
- **Ticket row** (`partials/ticket_row.html`) — issue-number badge linking to
  GitHub.
- **Ticket detail** (`ticket_detail.html`) — issue link, PR link, sync status,
  and `Title`/`Description` rendered read-only for linked tickets with
  "synced from GitHub #N".

## Error handling

- Every GitHub error is logged with ticket and repo context and surfaced in the
  UI via `LastError` or a parked outbox row. Nothing is silently swallowed —
  with the single deliberate exception of the idempotency-key unique violation,
  which is the de-dup mechanism working as designed.
- A failing repo poll never stops other repos.
- A failing push never blocks the ticket lifecycle.
- GitHub being entirely unreachable degrades to: no ingest, outbox rows
  accumulate and retry, tickets continue through their lifecycle unaffected.

## Testing

Per the project testing rules: TDD, 80%+ coverage, table-driven Go tests.

- **`internal/github`** — tests against `httptest.Server` returning recorded
  GitHub JSON payloads. Covers pagination, 304 handling, and error shapes.
- **`internal/orchestrator/ghsync`** — table-driven against `github.Fake` and
  in-memory SQLite.
  - Ingest: new labeled issue, updated issue, closed issue, label removed,
    duplicate issue in an overlapping cursor window, unlabeled issue ignored.
  - Outbox: success, transient failure and retry, backoff schedule, idempotency
    collision, parking after 8 attempts.
  - Reconcile: label drift corrected, issue-state drift corrected, comments not
    re-sent.
  - Manual sync: cooldown allowed / 429 / allowed after expiry, API-key caller
    rejected, trigger during an in-flight pass coalesced rather than doubled.
- **`internal/e2e/orchestrator_test.go`** — issue appears → ticket created →
  phase advances → comment and label enqueued and drained against the fake.
- **`internal/cli`** — the three new commands, including the `write: false`
  read-only guard.

**Test configuration note:** `poll_interval` and `drain_interval` must be
injectable. E2E tests set them to milliseconds; tests must never wait on the
real 15-minute ticker.

## Implementation phases

All four phases are in scope. This is ordering, not a scope reduction.

1. **Client, schema, ingest, outbox** — `internal/github` with its fake, the
   three model changes, the ingest pass, and outbox rows for labels, comments,
   and issue close. Includes the manual sync endpoint, cooldown, and
   `/settings/github`.
2. **Reconciliation** — the diffable-state correction pass.
3. **Branch push and PR creation** — the shem push, the `branch-pushed`
   endpoint, `pr` outbox handling, and push-credential documentation.
4. **CLI parity** — the three commands, the `.golem/config.yaml` block, and the
   `ensureRepoReady` write guard.

## Acceptance criteria

- Labelling an open GitHub issue `golem` produces exactly one Golem ticket in
  `unassigned`, within 15 minutes or immediately on manual sync.
- The same issue seen in two overlapping polls produces exactly one ticket.
- Editing an issue title on GitHub updates the ticket title within one cycle;
  the ticket's phase is unaffected.
- Advancing a ticket's phase results in exactly one `golem:<phase>` label on
  the issue and exactly one milestone comment, even across an orchestrator
  restart mid-drain.
- Closing the issue on GitHub closes the ticket; closing the ticket in the UI
  closes the issue.
- Reaching `ready-for-review` with push credentials configured pushes
  `ticket/<id>` and opens a draft PR containing `Closes #<issue>`.
- `no_push: true` suppresses the push and the PR, and the ticket still reaches
  `ready-for-review`.
- Manual sync inside the cooldown window returns 429; an API-key caller is
  rejected.
- GitHub unreachable for an hour causes no lost phase transitions, and queued
  writes land once it recovers.

---

# Amendment 1 — Intake approval for externally-ingested tickets

**Date:** 2026-09-15
**Status:** Approved, supersedes the "Work start" decision in the Decisions table.

## What changed and why

The original design chose **auto-start**: a labeled issue became an `unassigned`
ticket and a shem claimed it within seconds, with no human step. This amendment
replaces that with an **intake approval gate**: a ticket created from an
external integration enters a new `pending-approval` phase that no shem can
claim, and a human must release it before any agent runs.

The reason is a finding from Task 10's security review, independently verified:
`internal/shem/worker/executor.go` interpolates the ticket description **bare**
into all four agent prompts —

```
:395  buildBrainstormPrompt    Description: %s
:429  buildPlanPrompt          Description: %s
:457  buildImplementPrompt     Description: %s   ← drives shell and repo writes
:495  buildRevisePrompt        Description: %s
```

— with no delimiter and no treat-as-data framing. Before this feature a
description could only originate from an authenticated human using the
orchestrator's web form. After it, the description **is a GitHub issue body**,
authored by anyone who can open an issue in a synced repository.

Auto-start therefore meant stranger-authored text reaching an autonomous coding
agent with shell and repository write access, with no human in the loop. The
existing brainstorm and plan approval gates are real but insufficient: the
brainstorm phase itself runs on the raw description before any human sees
anything.

## The gate

- Ingest creates GitHub-sourced tickets in phase **`pending-approval`**.
- `pending-approval` is not claimable. Claimability is defined by
  `phase = 'unassigned'` in exactly two queries — the atomic claim
  (`api/tickets.go:52`) and the available list (`api/tickets.go:222`) — so the
  new phase is excluded by construction rather than by an added filter.
- Shems cannot set it: `validPhases` in `updatePhase` does not include it, and
  gains no new entry.
- A human releases the ticket from the dashboard, which transitions
  `pending-approval → unassigned`. From that point the existing flow is
  unchanged: a shem claims it and runs brainstorm as before.
- Tickets created through the orchestrator's own web form are unaffected and
  still enter at `unassigned`. The gate applies to externally-sourced tickets,
  identified by a non-nil `IssueNumber`.
- Closing the linked issue on GitHub still closes the ticket, including while it
  sits in `pending-approval`.

## What the gate does and does not buy

It guarantees a human sees the issue text before any agent does, and gives them
a place to reject obviously hostile or junk content. It does **not** guarantee
they will spot a subtle injection buried in a long issue body, which is why
Amendment 2 still applies.

## Amendment 2 — Prompt fencing (defence in depth)

Independently of the gate, the four prompt builders must mark externally-sourced
descriptions and fence them with explicit treat-as-data framing, so a released
ticket whose body carries an injection is still handled as data rather than as
instructions.

## Out of scope, recommended separately

Two items the security review raised that are repo-wide rather than this
feature's to decide:

- **DOMPurify** around `marked.parse`, or server-side sanitized rendering.
  `.Spec.Message` and `.Plan.Message` still render through an unsanitized
  `innerHTML` sink, and are now transitively reachable from external input
  (issue body → LLM prompt → `spec.md` → `.Spec.Message`).
- **A Content-Security-Policy header.** The orchestrator currently sets none,
  behind five third-party CDN script tags.

## Revised acceptance criteria

These replace the corresponding original criteria:

- Labelling an open GitHub issue `golem` produces exactly one ticket in
  **`pending-approval`**, within 15 minutes or immediately on manual sync.
- A `pending-approval` ticket is never returned by `/api/tickets/available` and
  can never be claimed, including under concurrent claim attempts.
- Releasing a `pending-approval` ticket moves it to `unassigned`, after which a
  shem claims it and the existing lifecycle is unchanged.
- No agent prompt is ever constructed from a ticket still in
  `pending-approval`.

---

# Amendment 3 — Sanitize the markdown render sink

**Date:** 2026-09-15
**Status:** Approved. Supersedes the "Out of scope, recommended separately"
note on DOMPurify in Amendment 1.

## The sink

`internal/orchestrator/ui/templates/layout.html` renders markdown client-side:

```js
el.innerHTML = marked.parse(el.textContent);
```

for every element carrying `md-content`. `marked@14` performs no sanitization —
it removed its own `sanitize` option years ago — and no sanitizer wraps it. The
`el.textContent` read also **decodes** Go's `html/template` escaping back to raw
text, so server-side escaping provides no protection for anything routed here.

Three fields currently render through it: `.Spec.Message` and `.Plan.Message`
(`ticket_detail.html`), and `.Message` (`partials/log_entry.html`).

## Why it matters now

Amendment 1 established that a GitHub issue body reaches `ticket.Description`,
and from there is interpolated into the prompts that produce `spec.md` and
`plan.md`. Those documents are posted back and rendered as `.Spec.Message` and
`.Plan.Message`. So untrusted external content reaches this sink **laundered
through an LLM**, and the design invariant the sink relied on — that only
trusted content reaches `md-content` — is now false by construction.

Removing `.Ticket.Description` from the sink (done previously) closed the direct
edge. It does not close the laundered one, and every field added to
`md-content` in future inherits the same exposure.

## The change

- Load DOMPurify from the CDN already used for `marked`, pinned to a major
  version in the same style.
- Sanitize `marked.parse`'s output before assigning it to `innerHTML`.
- **Fail closed.** If DOMPurify is unavailable — CDN blocked, script load
  failure, offline deployment — `renderMarkdown` must fall back to rendering the
  element's text as plain text, NOT to assigning unsanitized HTML. A missing
  sanitizer must degrade the display, never the safety.

## Not changed

`.Ticket.Description` stays plain text and out of `md-content`. It is the most
directly untrusted field in the system, a safe rendering already exists for it,
and defence in depth argues against making a single client-side sanitizer the
only thing standing between a stranger's issue body and the DOM. Restoring
markdown rendering for it is a deliberate future choice, not a consequence of
this amendment.

## Still out of scope

A `Content-Security-Policy` header. The orchestrator sets none, behind six
third-party CDN script tags. That is a deployment-wide decision beyond this
feature.

## Acceptance criteria

- `layout.html` loads DOMPurify at a pinned major version, before
  `renderMarkdown` can run.
- No assignment of unsanitized `marked.parse` output to `innerHTML` exists
  anywhere in the templates.
- With DOMPurify absent, `renderMarkdown` renders text and injects no HTML.

---

# Amendment 4 — Content-Security-Policy header

**Date:** 2026-09-15
**Status:** Approved. Supersedes the "still out of scope" note in Amendment 3.

The orchestrator sets no CSP, behind six third-party CDN script tags. CSP is the
layer that contains an XSS regardless of which sanitizer is or is not loaded —
it would have blunted the Amendment 3 sink even with DOMPurify absent.

## What the policy must permit

Enumerated from the templates rather than assumed:

| Need | Source |
|---|---|
| daisyUI stylesheet | `cdn.jsdelivr.net` |
| Tailwind Play CDN, marked, DOMPurify | `cdn.tailwindcss.com`, `cdn.jsdelivr.net` |
| htmx + SSE extension | `unpkg.com` |
| Iconify | `code.iconify.design` (script) and its icon API (fetch) |
| 6 inline `<script>` blocks | layout, ticket_detail, github_settings, ticket_new, dashboard |
| 9 `hx-on::after-request` attributes | htmx evaluates these itself |
| 1 native `onchange="this.form.submit()"` | `github_settings.html:33` |
| Runtime `<style>` injection | Tailwind Play CDN generates CSS in the browser |

## Design

**Inline scripts are allowed by hash, not nonce.** None of the six inline
`<script>` blocks contains a template action, so rendered content is byte-equal
to template source. Hashes are computed at startup from the template sources,
which makes the policy self-maintaining — editing a script updates its hash
automatically — and avoids threading a per-request nonce through eight `render`
call sites and every page's data shape.

**The native `onchange` handler must be removed**, replaced by a listener in
that page's existing inline script block. A single native inline handler forces
`script-src 'unsafe-inline'`, which would re-permit exactly the injected
`<img onerror=…>` vector this header exists to block, defeating the whole
policy.

**`'unsafe-eval'` is required and accepted.** Tailwind's Play CDN compiles CSS
at runtime, and htmx evaluates `hx-on` attribute bodies. Removing the need would
mean building Tailwind ahead of time and rewriting nine `hx-on` attributes —
both real refactors beyond this feature. `'unsafe-eval'` permits `eval` and
`new Function`; it does **not** re-permit inline `<script>` blocks or inline
event-handler attributes, so the protection that matters here is retained.

**`style-src` keeps `'unsafe-inline'`**, because Tailwind injects styles at
runtime. Style-only injection is a markedly lower-severity class than script
execution.

## Configuration

`csp.mode` takes `enforce` (default), `report-only`, or `off`.

Enforce is the default because a policy that protects nothing by default is not
protection. `report-only` emits `Content-Security-Policy-Report-Only` so an
operator can verify a deployment before enforcing, and `off` is an escape hatch
if an environment breaks.

## Known limitation

Go tests can assert the header is present, well-formed, and that its hashes
match the actual inline scripts. They cannot prove the dashboard still
functions under it — that needs a browser. A deployment should be checked
once with the browser console open, which `report-only` exists to make safe.

## Acceptance criteria

- Every HTML response carries a CSP header in `enforce` mode by default.
- `script-src` contains a `'sha256-…'` for each inline script and no
  `'unsafe-inline'`.
- No native inline event-handler attribute remains in any template.
- `report-only` switches the header name; `off` omits it entirely.
- A test computes the hashes from template sources and fails if the policy and
  the templates disagree.
