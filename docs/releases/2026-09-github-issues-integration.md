# Release note — GitHub Issues integration

Golem can now take work from GitHub issues and report progress back onto them:
issues carrying a trigger label become tickets, each phase change labels and
comments on the issue, and a finished ticket opens a draft pull request.

Setup is in the README under **Orchestrator + Shem → GitHub Issues**. This
note covers only what an operator needs to know when *upgrading an existing
deployment*.

---

## 1. Run the `body_hash` backfill — required for some upgrades

```sh
# rehearse first; this writes nothing
golem-orchestrator backfill body-hash --dry-run
golem-orchestrator backfill body-hash

# docker compose
docker compose run --rm orchestrator backfill body-hash --dry-run
docker compose run --rm orchestrator backfill body-hash
```

It reads `ORCHESTRATOR_DB` for the database path, like the `users` and `shems`
subcommands. It is idempotent, safe to run more than once, and safe to run
while the orchestrator is up.

**Who needs it.** If your database was last written by a release from *before*
this feature, you do not: those tickets have no linked issue and are
unaffected. You need it if you ever ran a build from partway through this
work — one where tickets already carried an issue number but the approval
gate's `body_hash` column did not exist yet — or if you restore a backup from
one.

**What happens without it.** Those tickets are permanently unclaimable. A
ticket can be approved in the dashboard and still never be picked up by any
shem, because claiming requires the approved text and the current issue text
to hash to the same value and one of them is blank. Approving again is
refused. Nothing heals it on its own: polling only revisits issues that have
actually been edited, and the reconciliation pass deliberately never writes
the ticket row.

**What it changes.** `body_hash`, on GitHub-linked tickets that have none, and
nothing else. It never marks anything approved, so it cannot release work no
human has read. A dry run prints exactly how many rows it would touch.

## 2. Dashboard pages left open across the upgrade fail once

State-changing requests from the dashboard now carry a CSRF token. A page that
was rendered by the old version does not have one, so the first button pressed
on such a page answers `403 forbidden: missing CSRF token`. **Reloading the
page fixes it.**

There is no upgrade step and nothing to migrate. The token is derived from the
session cookie rather than stored, so existing logins keep working and nobody
is signed out.

## 3. A repository whose default branch cannot be read now produces no tickets

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

## 4. Some issues get a milestone comment for work that already finished

Milestone comments ("Golem wrote a spec for this issue", and so on) are
delivered through an outbox whose delivered rows are kept forever, so anything
Golem has already posted is never posted twice. A ticket that was created by a
mid-upgrade build has no such history, so its next phase change can post a
comment about a milestone its work passed some time ago.

One-off, cosmetic, and limited to tickets that were already in flight. New
tickets are unaffected.

---

## Also in this release

- `docker compose` now passes `GOLEM_GITHUB_TOKEN` to both the orchestrator
  and the shem. Before, the variable was documented in `.env.example` and
  reached neither container, so the integration could not run from the
  documented quick start at all.
- GitHub sync starts whenever a token is present, rather than only when a
  repository was already enabled at startup. Enabling your first repository no
  longer needs a restart. Supplying the token for the first time still does —
  it is read once, at startup.
- **Sync now** says which failure it hit: the session expired, sync is not
  running on the server, the repository is not enabled, the repository is
  gone. Previously all four rendered "Failed — retry?".
- Writes to GitHub that fail repeatedly are parked and listed at the bottom of
  `/settings/github`, each with a **Retry** button. Before, a parked write was
  lost behind a single log line.
