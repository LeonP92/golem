# Tickets and Epics

Golem runs on one primitive: the **ticket**. Every change, from a one-line fix
to a multi-week feature, moves through the same lifecycle with the same gates.
An **epic** composes tickets: it turns one approved specification into a
dependency graph of ordinary tickets and drives them through that same
lifecycle until the whole graph is integrated.

This document covers both: what a ticket is, how it is gated, and how epics
plan, run, and integrate groups of them.

---

## Tickets

### What a ticket is

A ticket is a unit of work with its own git worktree and branch
(`ticket/<id>`), isolated from the base branch and from every other in-flight
ticket. Its state lives in the repo:

```
.golem/tickets/<id>/
  state.json   ← phase, branch, worktree path
  log.jsonl    ← append-only blackboard: every finding, blocker, resolution
```

In distributed mode the same ticket is created in the web UI and executed by a
shem (a worker node); the orchestrator holds the shared state in SQLite or PostgreSQL and
streams the blackboard to the dashboard as the shem works.

![Dashboard with tickets in every phase](images/dashboard.png)

### Lifecycle

```
unassigned → claimed → brainstorm → plan → implement → review → ready-for-review → closed
```

Two side states route work back for attention:

- **needs-attention** — a gate command failed, the reviewer's verdict was
  FAIL, or a required verdict is missing. A human (or a shem, after a
  decision) resumes the ticket from here.
- **revising** — a reviewer or tester sent the work back with feedback; the
  shem fixes on the same branch and re-enters review.

### Roles

Each phase runs a distinct role. Roles are markdown prompts owned by the repo
(`.golem/roles/`), so a project tunes its own process; the *contract* (what a
role must conclude) is enforced by Golem, not by the prompt.

| Role | When it runs | What it decides |
|---|---|---|
| `developer` | brainstorm, plan, implement | Writes the spec, the plan, and the code |
| `convention-enforcer` | after the last plan-step commit | Naming, structure, project patterns |
| `spec-adherence` | after the last plan-step commit | Scope creep, plan alignment |
| `reviewer` | at review | Full attestation; ends with `VERDICT: PASS` or `VERDICT: FAIL` |
| `tester` | after a flagged ticket is pushed | Judges the exact pushed head and records a verdict |

### The gate and the reviewer verdict

Review is fail-closed. `golem ticket review` (run by the shem in distributed
mode) executes the repo's `gate.commands` plus any acceptance commands the
ticket carries, records the result for the exact head, and then runs the
reviewer over the log and the branch diff:

- A failing gate command blocks the ticket.
- A reviewer `VERDICT: FAIL` blocks it the same way — a failed review cannot
  reach ready-for-review.
- A ticket that carries acceptance commands (every epic child does) also
  blocks when the reviewer produced no verdict at all.

Workers self-verify before asking for review: `golem ticket verify` runs the
gate locally and exits non-zero on failure, so a shem catches its own
regressions before the reviewer sees them. Self-verification runs on **every**
ticket by default, including tickets created before this behavior existed. A
ticket can opt out at creation with "Skip the worker's self-verification" on
the New Ticket form; an opted-out ticket goes straight from implementation to
the review gate and carries a "skipped at the ticket's request" badge on its
detail page.

### Approval modes

Per ticket, the human chooses how much control to keep:

- **Automatic (default)** — the shem advances through brainstorm and plan on
  its own validation and works to ready-for-review without pausing.
- **Manual** — tick "Require my approval after the spec and the plan" and the
  shem pauses after each of those phases until a human approves or requests
  changes in the UI.

![Creating a ticket: approval mode, tester flag, self-verification opt-out, model selection](images/ticket-new.png)

### The tester gate

The tester is a per-ticket flag, set at creation with "Run the tester on the
pushed head". When it is on, the shem runs the `tester` role after the
ticket's branch is pushed; the tester judges that exact head against the
ticket's acceptance scenarios and records a structured verdict on the ticket.
Verdicts are append-only and bound to the head SHA — only the newest verdict
*for that exact head* counts. A changed head needs a fresh verdict; it does
not inherit an earlier pass. External pushes do not start a tester run. The detail page shows whether the tester is required and the
latest verdict with the head it judged.

Epics compose the same flag: an epic with auto-merge children sets it on
every child it creates, because the integration lane may only merge a head
the tester passed.

### The activity log

Every role writes to the ticket's blackboard. The orchestrator renders it as a
live activity log — specs, plans, gate results, verdicts, blockers — so a
human can audit not just *what* changed but *why the process allowed it*.

![Ticket detail: the tester verdict bound to the pushed head, and the live activity log](images/ticket-detail.png)

---

## Epics

### What an epic is

An epic turns one specification into several dependent tickets and runs them
as a single workflow. Children are ordinary tickets — same phases, same roles,
same gates — with three additions: they know which epic and node they belong
to, their gate includes the node's acceptance commands, and their merged SHA
is recorded as the receipt that unblocks their successors.

### Creating an epic

An epic is created from a repository, a base branch, and a Markdown
specification. The spec is stored verbatim and hashed; every plan revision
binds back to it. Creation also fixes the run's topology and policy:

- **Branch strategy** — `epic-branch` integrates children into a dedicated
  `epic/<id>` branch; `trunk-flags` integrates them straight into the base
  branch.
- **Delivery target** — `feature-branch` (the run ends in an integrated branch
  a human reviews; the GitHub addon can open its final pull request) or `base-branch` (delivery *is* the base branch;
  required by `trunk-flags`).
- **Auto-merge children** — the shem merges the exact tested child head into
  the integration branch once every gate holds.
- **Auto-approve child phases** — children inherit automatic approval. The
  choice is snapshotted into each child when it is created; editing the epic
  later never changes a child already running.

Auto-merge children makes the tester gate mandatory: no child merges without
a tester pass. For `epic-branch` runs, the final merge to base is a human step.

![Creating an epic: strategy, delivery target, and merge policy](images/epic-new.png)

### The plan

The plan is the contract the run executes. It is submitted as JSON with three
parts:

- **Requirements** — the numbered outcomes the spec demands. Every requirement
  must be covered by at least one node, and every node must exist for at
  least one requirement: no uncovered outcome, no orphan work.
- **Nodes** — the proposed tickets, each with a stable key, title, scope,
  non-goals, acceptance commands, and the requirements it serves.
- **Edges** — hard dependencies: the predecessor must be integrated before
  the successor may start.

Plans are validated on submission: well-formed keys, full requirement coverage
in both directions, no cycles, at most 200 nodes. Every save is a new
immutable revision. A human then **approves** a specific revision — the
approval binds the approver's name to the revision *and* its content hash, so
"approved" always means "this exact plan".

### The run

Starting an approved epic materializes each node into an ordinary ticket
(`epic-<id>-<key>`). From then on the run is just tickets, plus scheduling:

- A child is claimable only while the run is `running` and all of its
  predecessors are integrated. The dashboard shows this directly: children
  carry an epic badge, and blocked children show what they are waiting on.
- Children work through brainstorm → plan → implement → review exactly like
  standalone tickets. Their acceptance commands run in the gate.
- Children of an auto-merge run carry the tester flag, so the shem runs the
  `tester` role against the pushed head and posts its verdict (see *The
  tester gate* above).

**Pause** and **Resume** stop and restart claims and merges at the next
attempt; work already claimed finishes.

![An epic run: approved plan, requirements, and children in dependency order](images/epic-run.png)

The run view reads the same on a phone:

![Epic run on mobile](images/epic-run-mobile.png)

### The integration lane

Integration is how a child's code becomes permanent, and it always ends in a
**receipt**: the merged SHA recorded on the child. A receipt is idempotent —
the same SHA again is a no-op — and a *different* SHA for an already
integrated node is rejected as a conflict. Receipts are what readiness is
computed from, so a successor unblocks exactly when its predecessors' code
has actually merged, never when a ticket merely looks done.

The orchestrator gates each exact tested head and grants one fenced integration
lease per epic. The shem merges through a shared bare Git mirror and pushes
with a lease against the target's previous SHA. The orchestrator then records
the receipt and releases dependents atomically. Child integration does not
require GitHub or child pull requests.

- Tester **FAIL** returns the child to `revising` with its evidence.
- A merge conflict returns the child to `revising` to resolve against the
  integration branch; the resolved head must pass the tester again.
- Tester **inconclusive** requires attention. Operational Git failures can be
  retried by reconciliation without accepting an unverified receipt.
- A changed head needs a fresh tester verdict. External pushes do not
  automatically trigger a tester run.

Pause blocks new integration leases. Receipts for a merge already in flight
can still be recorded. Local/no-push runs use the same gates and receipts
against local refs.

Manual integration uses the authenticated management API. This is a trusted
human attestation that the code was merged, not an automatic tester-gated
merge; it records the supplied SHA without performing Git verification:

```
POST /api/epics/{id}/nodes/{key}/integrate   {"sha": "<merged-sha>"}
```

### Finishing

When every node is integrated:

- `epic-branch` runs move to `awaiting-final-review` and retain the completed
  `epic/<id>` branch. With the GitHub addon enabled, a final pull request
  to base presents the result. The final merge is a human step: a
  person reviews the whole run as one diff, merges it, and records the final
  base SHA through the UI or API.
- `trunk-flags` runs are already on base, so the last receipt completes the
  run.

Run states: `draft → awaiting-approval → running ⇄ paused →
awaiting-final-review → completed`.

![The epics list: state, strategy, and plan status at a glance](images/epics-list.png)

---

## HTTP API

Everything the UI does is available over the API. Epic management endpoints
are session-authenticated; tester verdicts are posted by the shem that holds
the child.

| Endpoint | Purpose |
|---|---|
| `POST /api/epics` | Create an epic (repo, spec, strategy, policy) |
| `GET /api/epics` · `GET /api/epics/{id}` | List epics; read one with its current plan |
| `POST /api/epics/{id}/revisions` | Submit a plan revision (validated, immutable) |
| `POST /api/epics/{id}/approve` | Approve a revision, bound to its plan hash |
| `POST /api/epics/{id}/start` | Materialize the approved plan into tickets |
| `POST /api/epics/{id}/pause` · `.../resume` | Stop and restart claims and merges |
| `POST /api/epics/{id}/nodes/{key}/integrate` | Record an integration receipt by hand |
| `POST /api/epics/{id}/verdicts` | Post a tester verdict (shem-authenticated) |
| `POST /api/epics/{id}/merge-final` | Record the human final merge attestation (`{"sha":"<base-head>"}`); does not perform a merge |

---

## Worked example

A spec asks for usage-based billing: record usage durably, charge
idempotently, invoice monthly, reconcile drift. The plan decomposes it into
four nodes behind four requirements:

```json
{
  "requirements": [
    {"id": "R1", "text": "Usage events are recorded durably and priced per plan tier"},
    {"id": "R2", "text": "Charges are idempotent: a retry with the same key never double-charges"},
    {"id": "R3", "text": "Monthly invoices match the payment provider total"},
    {"id": "R4", "text": "A reconciliation job can replay any past month and report drift"}
  ],
  "nodes": [
    {"key": "usage-schema",      "title": "Usage event schema and ingestion",
     "acceptance_commands": ["make build", "make test"], "requirement_ids": ["R1"]},
    {"key": "pricing-charges",   "title": "Tier pricing and idempotent charges",
     "acceptance_commands": ["make build", "make test"], "requirement_ids": ["R1", "R2"]},
    {"key": "monthly-invoicing", "title": "Monthly invoice generation",
     "acceptance_commands": ["make build", "make test"], "requirement_ids": ["R3"]},
    {"key": "reconciliation",    "title": "Reconciliation replay job",
     "acceptance_commands": ["make build", "make test"], "requirement_ids": ["R4"]}
  ],
  "edges": [
    {"predecessor": "usage-schema",      "successor": "pricing-charges"},
    {"predecessor": "usage-schema",      "successor": "monthly-invoicing"},
    {"predecessor": "pricing-charges",   "successor": "reconciliation"},
    {"predecessor": "monthly-invoicing", "successor": "reconciliation"}
  ]
}
```

The run plays out as:

1. `usage-schema` is the only claimable child. A shem takes it through the
   full lifecycle; its reviewer and tester pass and its tested head merges into
   `epic/<id>` — receipt `8c41d0f` recorded.
2. That receipt unblocks `pricing-charges` and `monthly-invoicing`, which two
   shems can now run in parallel. `reconciliation` stays blocked until both
   integrate.
3. The tester passes `pricing-charges` at head `4f9c2e1`; the lane merges it
   pinned to that head. `monthly-invoicing` fails review once, goes to
   `revising` with the reviewer's notes, and passes on the next attempt.
4. The last receipt (`reconciliation`) moves the run to
   `awaiting-final-review`. A human reviews the single `epic/<id>` → `main`
   pull request — the whole run, one diff — and merges it.

Every step above happened through the same ticket lifecycle a standalone
ticket uses. The epic added the plan, the scheduling, and the receipts —
nothing else.
