# Release note — GitHub Issues integration

Golem can now take work from GitHub issues and report progress back onto them:
issues carrying a trigger label become tickets, each phase change labels and
comments on the issue, and a finished ticket opens a draft pull request.

Setup is in the README under **Orchestrator + Shem → GitHub Issues**. This
note covers only what an operator needs to know when *upgrading an existing
deployment*.

---

## 1. Run the `body_hash` backfill — before you start the new binary

```sh
# rehearse first; this writes nothing
golem-orchestrator backfill body-hash --dry-run
golem-orchestrator backfill body-hash

# docker compose
docker compose run --rm orchestrator backfill body-hash --dry-run
docker compose run --rm orchestrator backfill body-hash
```

It reads `ORCHESTRATOR_DB` for the database path, like the `users` and `shems`
subcommands, and it is idempotent — running it twice changes nothing the
second time.

**Who needs it.** If your database was last written by a release from *before*
this feature, you do not: those tickets have no linked issue and are
unaffected. You need it if you ever ran a build from partway through this
work — one where tickets already carried an issue number but one or both of
the approval gate's hash columns did not exist yet — or if you restore a
backup from one.

**What happens without it depends on how far through you were, and one of the
two cases is not a safe one.** A schema migration adds each missing column at
its zero value, so which shape your rows land in depends on which columns the
build that last wrote them knew about:

| Your last build had | Approved tickets land as | Consequence |
|---|---|---|
| `issue_number` only | `intake_approved=0`, both hashes blank | **Unclaimable.** Fail-closed. |
| `+ intake_approved` | `intake_approved=1`, both hashes blank | **Was claimable with no binding at all.** Fail-open. |
| `+ approved_body_hash` | `intake_approved=1`, approved hash set, `body_hash` blank | **Unclaimable.** Fail-closed. |

The two fail-closed rows are an annoyance: a ticket can be approved in the
dashboard and still never be picked up by any shem, because claiming requires
the approved text and the current issue text to hash to the same value and one
of them is blank. Approving again is refused. Nothing heals it on its own —
polling only revisits issues that have actually been edited, and the
reconciliation pass deliberately never writes the ticket row.

The middle row is the one that matters, and it is the opposite problem.
An earlier version of this note described every affected ticket as
"permanently unclaimable". For that shape it was backwards. Both hashes were
blank, the claim predicate compared them for equality, and the empty string
equals itself — so the ticket was **immediately claimable by any shem, with no
hash binding whatsoever**. Worse, the build that produced those rows had no
re-gate on its polling either, so if the issue was edited after the approval,
the text sitting on that ticket is text no human has ever read.

**This release closes that hole in the predicate itself**: claiming now
requires a non-blank approved hash as well as a matching one, so a blank-hash
row is refused no matter how it got there. You do not have to win a race with
your own shems. But run the backfill **before you start the new binary
anyway**, for two reasons: it is what makes those tickets usable again, and if
you ever roll back to an intermediate build the hole is open again for as long
as that build is serving.

**What the backfill does.**

1. It writes `body_hash` on GitHub-linked tickets that have none, hashing that
   row's own stored description with the same function ingest uses. It never
   recomputes a hash that is already there.
2. It returns to `pending-approval` any GitHub-linked ticket that is marked
   approved but carries a blank approved hash — the middle row above. Those
   approvals were recorded before there was anything to bind them to, so they
   are not approvals of any particular text and cannot be honoured. This step
   is what un-sticks them: without it the new predicate refuses the ticket and
   the dashboard cannot re-approve it either, because approval is a one-way
   column that only the polling loop would otherwise clear. Tickets a shem is
   already working on, and closed tickets, are left alone.

It only ever **clears** approval, never grants it, so it cannot release work no
human has read. A dry run prints exactly how many rows each step would touch.

**What you will see afterwards.** The tickets from step 2 are back in
`pending-approval` with their current issue text, waiting for one human read
each. That is the correct end state: it is the re-approval you would have been
asked for at the time, if the build you were running had had anything to
record it against. Tickets repaired by step 1 alone become claimable again
immediately, on exactly the text their recorded approval was given for.

**How it interacts with the re-hash below.** The first poll after upgrading
re-composes every linked ticket's description with its title and re-gates them
once (see section 2). Both steps are correct and the order does not matter;
a ticket already sent back by step 2 simply stays there.

## 2. Approved tickets go back to "pending approval" once, after the first poll

**Expect a wave of what looks like un-approvals on your first poll after
upgrading. It is not a fault, and it clears as soon as each ticket is
re-approved.**

A ticket's description is now the issue's **title and body** — the title, a
blank line, then the body — where it used to be the body alone. That is what
the CLI has always stored, and it means an agent finally sees the title: an
issue whose title says everything and whose body is empty used to become a
ticket with an empty description and an agent with nothing to work on.

The approval you give is bound to the exact text you were shown, by a hash. On
the first poll after upgrading, every GitHub-linked ticket's description gains
its title, so that hash changes for all of them. Any ticket that was approved
but **not yet claimed** therefore returns to `pending-approval` for one
re-read. Tickets already claimed are left alone — the shem working on one is
not interrupted, and the change is recorded in that ticket's log.

This is fail-closed and is arguably just correct: the text under approval
genuinely is changing, by gaining a line nobody approved. Re-approving is one
click per ticket and is a one-time event.

It also closes a real hole, which is why it was not made optional. While the
hash covered the body alone, editing only an issue's **title** moved nothing:
an approved ticket stayed approved, and — now that the title reaches the agent
— it would have carried text no human had read. Editing a title now re-gates
exactly as editing a body does.

## 3. Dashboard pages left open across the upgrade fail once

State-changing requests from the dashboard now carry a CSRF token. A page that
was rendered by the old version does not have one, so the first button pressed
on such a page answers `403 forbidden: missing CSRF token`. **Reloading the
page fixes it.**

There is no upgrade step and nothing to migrate. The token is derived from the
session cookie rather than stored, so existing logins keep working and nobody
is signed out.

## 4. A repository whose default branch cannot be read now produces no tickets

Golem asks GitHub for a repository's default branch when it first turns an
issue into a ticket, and uses it as the pull request's base. That lookup used
to fall back to `main` and swallow the error, which quietly pinned tickets to
the wrong base on any repository whose default is `master`, `develop` or
`trunk`. It now fails the ingest of that issue instead.

The practical difference: if the lookup keeps failing — a revoked token, a
permissions change, a persistent rate limit — **that repository stops
producing tickets entirely** rather than producing tickets with a wrong base
branch. The next successful poll picks up everything it skipped; nothing is
lost.

It can be experienced as "Golem stopped working", so check the **Status**
column on `/settings/github` first. The reason is recorded there on the
repository's row.

## 5. Some issues get a milestone comment for work that already finished

Milestone comments ("Golem wrote a spec for this issue", and so on) are
delivered through an outbox whose delivered rows are kept forever, so anything
Golem has already posted is never posted twice. A ticket that was created by a
mid-upgrade build has no such history, so its next phase change can post a
comment about a milestone its work passed some time ago.

One-off, cosmetic, and limited to tickets that were already in flight. New
tickets are unaffected.

## 6. Run exactly one orchestrator process against any one database

This has always been true and has never been written down outside a Go comment,
which is the wrong place for it: the person who would violate it reads the
README, not `publish.go`.

Shems scale out. The orchestrator does not. The GitHub write outbox is drained
by a select that takes no lock and writes no claim — no `FOR UPDATE SKIP
LOCKED`, no claim column, no lease — so two orchestrators against one database
both select the same due rows and both deliver them. You get duplicate
milestone comments and duplicate label writes on your issues, burnt rate limit
on a feature designed around conserving it, and no error anywhere, because each
process marks the row done and considers the job well done. The issue polling
has the same shape.

It matters now because this release adds PostgreSQL support, and running more
than one process is the usual reason to reach for PostgreSQL. It is written up
in the README under **One orchestrator process per database**, and repeated in
`deploy/orchestrator.yaml`.

For availability, run one process and restart it. The outbox is durable:
anything queued while it is down is delivered when it comes back.

---

## Also in this release

- Agents are shown the issue **title**, not just the body, and the CLI and the
  orchestrator now produce byte-identical descriptions from the same issue.
- `docker compose` now passes `GOLEM_GITHUB_TOKEN` to the orchestrator.
  Before, the variable was documented in `.env.example` and reached neither
  container, so the integration could not run from the documented quick start
  at all. The shem's push credential is a **separate** variable,
  `GOLEM_SHEM_GITHUB_TOKEN`, empty unless you set it: the shem container is
  where the agent runs, on issue text anyone can write, so it is handed a
  repository credential only when someone has decided it needs one — the same
  moment they turn `no_push: false` on. Set it to a push-only token if you can.
- The agent subprocess runs with an allow-listed environment rather than
  Golem's own, on **both** paths — the shem's `claude --print` and CLI mode's
  `golem observer dispatch` / `golem ticket review`. It keeps its model
  credentials, the shell, locale, proxy and TLS settings, git identity, and the
  language toolchains; it no longer sees anything in the `GOLEM_` namespace.
  Golem's own subprocesses, including the shem's `git push`, are unaffected. If
  a build in your repository needs a variable the list does not cover, name it
  in `GOLEM_AGENT_ENV` (comma-separated); that cannot re-add the `GOLEM_`
  namespace.
- Logging out is a `POST`. A `GET /logout` used to be reachable from rendered
  markdown via `![](/logout)`, which logged the operator out on page load.
- DOMPurify, the sanitizer on the markdown sink, is pinned to an exact version
  with a Subresource Integrity hash instead of floating on `@3`.
- GitHub sync starts whenever a token is present, rather than only when a
  repository was already enabled at startup. Enabling your first repository no
  longer needs a restart. Supplying the token for the first time still does —
  it is read once, at startup.
- **Sync now** says which failure it hit: the session expired, sync is not
  running on the server, the repository is not enabled, the repository is
  gone. Previously all four rendered "Failed — retry?". When sync is not
  running it names the variable *your* config reads (`github.token_env`),
  not a hardcoded one.
- Writes to GitHub that fail repeatedly are parked and listed at the bottom of
  `/settings/github`, each with a **Retry** button. Before, a parked write was
  lost behind a single log line.
