# internal/cli

The cli module is the command layer of Golem, exposing every user-facing subcommand as a top-level Go function that accepts parsed flag arguments and io.Writer streams for stdout/stderr, returning an integer exit code. Each command function loads configuration and ticket state, delegates all domain logic to internal packages (ticket, blog, graph, soul, wiki, workspace, observer, gate, askwait, agentrunner), and wires results back to the caller. The module owns no business logic itself; its role is pure orchestration — selecting backends, sequencing cross-package calls, and surfacing errors.

## Functions

- TicketAdvance
- TicketReview
- TestTicketAdvanceChangesPhase
- TestTicketAdvanceRejectsUnknownPhase
- TestTicketReviewRunsGateAndSetsReadyForReview
- TestTicketReviewSetsNeedsAttentionOnGateFailure
- NewRunner
- TestNewRunnerReturnsClaudeCodeForClaudeCodeBackend
- TestNewRunnerRejectsUnknownBackend
- TicketClose
- TestTicketCloseTearsDownWorktreeAndSetsClosed
- TestTicketClosePromotesSoulEntryFromDivergence
- TestTicketCloseIgnoresMalformedSoulLines
- GraphBuild
- GraphCheckBoundary
- GraphDeps
- GraphStatus
- GraphUpdate
- GraphWhoImports
- Init
- TestInitRequiresBackendFlag
- TestInitCreatesGolemLayout
- LogEmit
- Ask
- Answer
- TestLogEmitAppendsEntry
- TestLogEmitRejectsQuestionAndAnswerTypes
- TestAskThenAnswerRoundTrip
- TestAskReturnsAnswerWhenPostedInTime
- ObserverDispatch
- TestObserverDispatchAppendsFindingFromRealCommit
- TicketResume
- TestTicketResumeShowsPhaseAndLastLogEntry
- TestTicketResumeMissingTicketFails
- SetStep
- CheckBloat
- TestSetStepUpdatesExpectedLines
- TestCheckBloatFlagsCommitFarOverExpectation
- TestCheckBloatStaysSilentWithinExpectation
- TicketNew
- TestTicketNewCreatesStateWorktreeAndLog
- TestTicketNewTrivialFlagSkipsBrainstorm
- TestTicketNew_WithTicketID
- Tickets
- TestTicketsListsAllTicketsWithPhase
- TestTicketsHandlesNoTicketsDir
- WikiSearch
- WikiRebuild
- TestWikiRebuildThenSearchFindsRankedMatch
- TestWikiSearchRequiresQuery

## Imports

flag, fmt, io, os, path/filepath, github.com/leonp92/golem/internal/agentrunner, github.com/leonp92/golem/internal/blog, github.com/leonp92/golem/internal/config, github.com/leonp92/golem/internal/gate, github.com/leonp92/golem/internal/ticket, bytes, testing, strings, github.com/leonp92/golem/internal/graph, github.com/leonp92/golem/internal/soul, github.com/leonp92/golem/internal/workspace, sync, github.com/leonp92/golem/internal/roles, os/exec, errors, slices, time, github.com/leonp92/golem/internal/askwait, github.com/leonp92/golem/internal/observer, runtime, bufio, github.com/leonp92/golem/internal/bloat, github.com/leonp92/golem/internal/wiki
