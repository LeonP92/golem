# Epic workflow verification

Verified 2026-10-16/17 on `review/epic-full`, starting from `b2ab571`.
The actual worktree is `C:/Users/leonp/Documents/code/golem-epic-full`.
The initial worktree was clean. All five architecture-review commits were present
(`1370aa3`, `7af8f66`, `3cc8b4f`, `18d5a08`, `b2ab571`).
Prior remote CI claims were not independently checked; the local CI-equivalent
checks below were reproduced against the resulting source.

## Preserved environment and evidence

All original sandboxes and their volumes/processes were retained. Ports
18081–18083 and regression ports 18091–18093 were inspected before changes.
Only the regression Codex CLI was updated in place, retaining its original binary
at `/tmp/golem-before-recovery`. No orchestrator or shem daemon was restarted.
All Git pushes performed by E2E used the local fake-origin fixture, not GitHub.

The saved regression artifacts were under:
`%LOCALAPPDATA%/Temp/claude/C--Users-leonp-Documents-code-golem/0ae517bf-5de6-4065-9506-9c875a2a7b11/scratchpad/reg`.
They showed a completed OpenCode epic, but a failed Codex epic and a pending
Claude approval. A blanket prior E2E pass would have been incorrect.

Retained screenshots and concise check results are in
[docs/testing/epic-verification](docs/testing/epic-verification/).
Browser storage state, credentials, sandbox configuration, and databases are
excluded. Screenshots cover ticket/epic pages, not credential pages.

## Verified results

| Scope | Result and evidence |
| --- | --- |
| Ordinary Codex ticket | Saved ticket `e4645573-2f5d-4cbd-bdfe-c9be8b893c5e` completed implementation and structured reviewer PASS; subsequently closed. |
| Ordinary OpenCode ticket | Saved ticket `460f2024-f521-405d-b539-2445c78eee0c` completed implementation and structured reviewer PASS; subsequently closed. |
| Ordinary Claude ticket | Legacy VERSION ticket `5a4e7cfb-4480-4853-914d-9eefb26b788a` reached review and was closed. Its saved log also records a fake-origin ownership push failure; this is not proof of a complete current-branch run. |
| Claude approval/resume | Ticket `8bf2ddb9-d332-438b-b2e5-b76b21eb1d42` resumed its pending plan approval through the UI and entered implementation. Claude then refused execution because its weekly usage limit was reached. Current-branch completion remains externally blocked. |
| Old main database upgrade | Built a fresh database by running actual `a706b71` `db.Open` and model code exported with `git archive`. Migrated it with the branch code. Five tickets and checkpoint/ownership data survived; ordinary available, resumable, claim and revise-claim APIs passed; reopening was idempotent. |
| Current local origin/main upgrade | Repeated the same procedure using actual `36efd04` code. All preservation and API checks passed. No hand-written schema was used to create either database. |
| OpenCode epic | Epic `bc80b964-a6fd-468f-b463-f27ce57733fd`: two children received structured exact-head tester PASS verdicts and integration receipts; the dependent child started from the first merge. State reached `awaiting-final-review`. |
| Codex reviewer | Original epic `d72e92cf-f831-438c-9f54-dcbd685b4b99` correctly blocked on a missing-plan observer finding. After fixing observer evidence, new epic `7af65f78-7929-4bab-a99b-f281fe1b9a63` produced structured reviewer PASS. |
| Codex tester fail → fix → retest → merge | Injected wrong `wave.sh` output into the local test branch (`746d2866`). The real patched tester returned FAIL with acceptance exit 1 and `got: broken`. The existing monitor dispatched revising; the real worker fixed it in `2ed81b26`. Patched tester returned PASS for that exact head, and the monitor recorded merge receipt `433ea6bd053eee3604dbe02d2b3ab8a774037576`, unblocking child B. |
| Self-verification | Initial Codex child recorded actual acceptance execution (`sh tests/wave_test.sh`, exit 0). Recovery regression tests now prove the approved acceptance commands survive revision and cold recovery, closing the discovered case where revision silently ran zero acceptance commands. |
| Pause/resume | New Codex epic was started, paused and resumed through authenticated API calls; paused/running screens were captured. Automated API and monitor tests cover claim/revise/reclaim and merge refusal while paused. |
| UI/API approval binding | Epic `28ac1829-45ea-4b77-9b43-f39b43cbac85` was created and approved in the UI. Start before approval returned 409. Saving revision 2 cleared approval; stale revision-1 API approval returned 409, and exact revision-2 API approval succeeded. This fixture was deliberately not started. |
| Manual final merge | Golem merge-final returned 409 with final auto-merge disabled. OpenCode final PR #3 was then explicitly merged through the local fake GitHub API at `3154bcd784e29f6b1aed45011d3cb8b03a9000c5`. No background final merge was added. |
| Legacy shems | Regression proves a worker lacking `supports_epics` cannot list, claim, resume, revise or reclaim epic children, but can claim ordinary tickets. Re-registering an older binary clears a previously advertised capability. |
| Concurrent receipts | PostgreSQL regression submits both receipts concurrently and verifies neither is lost and the run reaches final review. Receipt reconciliation now serializes on the epic row. |

### Runtime verification limit

To preserve the running sandboxes, patched worker functions were built into a
temporary live test entry point and invoked inside the existing Codex container.
It called the production recovery/tester code and authenticated verdict API.
The existing daemon, monitor and revision agent stayed running. Consequently,
this verifies the patched production stages and integration path, but is not a
fresh deployment/restart soak test of the final worker binary. The old daemon's
original tester inconclusive results remain in the history rather than being
hidden by prompt changes.

## Bugs fixed

1. Observers lacked the saved spec, plan and approval log. They now receive those
   artifacts through the existing escaped data prompt, with a regression test.
2. Codex tester could not judge correctness because the worker supplied neither
   source diff nor comparison base. It now receives the pinned base, exact-head
   diff and escaped acceptance evidence. Missing base fails closed.
3. Revision/recovery overwrote state with a partial duplicate struct, losing the
   pinned base, branch and acceptance commands. It now reuses `ticket.State`,
   preserves local metadata and restores approved acceptance commands from the
   claim. Cold epic recovery derives a comparison base from fetched history.
4. Old shems could claim epic children without understanding the tester stage.
   A default-false registered capability now gates every ownership path.
5. Simultaneous PostgreSQL receipts could each reconcile before seeing the
   other's write. Per-epic serialization prevents lost readiness/final state.
6. Acceptance environments retained Codex/OpenAI/OpenCode credential variables.
   Those prefixes are now stripped and covered by the existing environment test.

## Checks and review

- `go test -race ./...` passed with an isolated PostgreSQL 18 service enabled.
- All four PostgreSQL tests passed, including concurrent receipt integration.
- `go vet ./...` and `go build ./...` passed.
- Pinned `golangci-lint v2.13.2`: **0 issues**.
- `git diff --check` passed.
- Golem bloat check, convention-enforcer and spec-adherence reviews ran in an
  isolated source fixture; final reviewer verdict: **PASS**.
- A first Golem finding interpreted “manual final merge” as removal of even the
  human-triggered API/UI action. Tracing all production callers showed only
  authenticated human handlers invoke `MergeFinal`; the existing manual design
  was preserved and that finding was resolved explicitly in the review log.
- [Ponytail review](docs/testing/epic-verification/ponytail-review.md) checked the
  changed paths for correctness, concurrency, reuse and unnecessary code.

## Remaining issues and limits

- **Human pushes are not automatically retested.** A new PR head has no matching
  tester verdict, so the lane waits safely. A person must request a revision
  and ensure the remote changes are incorporated; automatic fetch/retest of
  arbitrary human pushes is not implemented. Do not assume unattended retesting.
- Claude's current-branch approval/resume completion is blocked by its external
  weekly quota. Do not report this scenario as passing.
- Feature-branch delivery remains `awaiting-final-review` after an external
  manual final merge; there is no background completion reconciliation for that
  human-managed handoff. This existing design was preserved.
- Shell-only fixtures have no supported source graph. Their graph-update
  warnings were recorded; they are not acceptance failures or evidence that a
  graph was successfully built.
- The original failed Codex epic is retained for diagnosis. No rebase, reset or
  deletion was used to erase its failure history.

Final dependent-child retest: the patched real tester returned PASS for B at 
head 1a83b305f9022fb310eba95b8af6b67b998f6a1c, base 
433ea6bd053eee3604dbe02d2b3ab8a774037576. Its acceptance command 
sh tests/loudwave_test.sh exited 0 with output ok. No additional model reviews 
were started after the request to conserve usage.

Both Codex children now have integration receipts. Child B receipt is 
5adbd32b230ea75f725f6d4ff079f65f64acbb3e; epic 
7af65f78-7929-4bab-a99b-f281fe1b9a63 reached awaiting-final-review. 
No additional active implementation remains in this recovery epic.
