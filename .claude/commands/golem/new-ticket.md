---
name: golem-new-ticket
description: Start a new Golem ticket - brainstorm, plan, implement, review
---

Given a one-line description of what to build:

1. Run `golem ticket new --id <slug> "<description>"` to create
   the ticket, worktree, and branch.
2. Brainstorm a spec with the human, following `.golem/roles/spec-adherence.md`.
   Get explicit approval before continuing.
3. Run `golem ticket advance --ticket <id> --to plan`.
4. As the developer (`.golem/roles/developer.md`), produce ordered
   plan steps, each with an expected diff-line-count. Get human approval.
5. Run `golem ticket advance --ticket <id> --to implement`.
6. Execute each plan step: before starting, run
   `golem ticket set-step --ticket <id> --expected-lines <n>`; commit
   at each small logical unit; after every commit, run
   `golem ticket check-bloat` and the two `golem observer dispatch`
   calls from `.golem/roles/developer.md`; check the log for
   unresolved BLOCKERs before continuing.
7. When all steps are done, run
   `golem ticket review --ticket <id>` and report the result to the
   human.
8. Once the human approves the reviewed work, run
   `golem ticket close --ticket <id>` — this promotes any
   generalized soul entries the reviewer proposed and tears down the
   worktree.
