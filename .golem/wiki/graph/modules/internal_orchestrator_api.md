# internal/orchestrator/api

This module implements the HTTP API layer for the Golem orchestrator server, exposing REST endpoints consumed by both Shem agents (authenticated via API key) and human operators (authenticated via session cookie). It is organized into route groups: ticket lifecycle management (create, list, get, claim, revise-claim, phase/checkpoint updates), Shem registration and WebSocket upgrade, log ingestion and SSE streaming, and human-input CRUD plus a unified ticket action dispatcher that handles approve, requeue, close, needs-attention, request-changes (including a review-time revising transition), and answer actions. The Handlers struct is the central dependency carrier holding a GORM database handle, a WebSocket hub for pushing real-time messages to connected Shems, an SSE broker for streaming log events to browser clients, and an optional HTML renderer for log entries. Ticket claiming and phase/checkpoint updates enforce ownership preconditions atomically via conditional row updates, and most state transitions fan out notifications to relevant Shems via WebSocket broadcast or push.

## Functions

- RegisterHumanRoutes
- TestApproveTicket_TransitionsPhase
- TestApproveTicket_PlanToImplement
- TestApproveTicket_WrongPhase
- TestAnswerHumanInput_ResolvesAndLogs
- TestAnswerHumanInput_MissingFields
- TestRequeueTicket_TransitionsToUnassigned
- TestRequeueTicket_WrongPhase
- TestRequeueTicket_AnyActivePhase
- TestCloseTicket_TransitionsToClosed
- TestCloseTicket_WrongPhase
- TestNeedsAttentionTicket
- TestRequestApproval_CreatesHumanInput
- TestRequestApproval_EmptyPrompt
- TestPendingApproval_ReturnsPending
- TestActionRequestChanges_FromReadyForReview_MovesToRevisingAndPushes
- TestActionRequestChanges_FromReadyForReview_NoAssignedShem_Conflict
- TestActionRequestChanges_AlreadyRevising_FallsThroughTo404
- TestActionRequestChanges_Brainstorm_ResolvesApprovalNoPhaseChange
- TestPendingApproval_NoneReturnsEmpty
- TestPendingHumanInput_ReturnsOldest
- TestAckHumanInput
- RegisterLogRoutes
- TestPostLog_AssignsSequenceNum
- TestPostLog_CreatesHumanInput_WhenToRoleHuman
- NewHandlers
- RegisterShemRoutes
- TestRegisterShem
- TestClaimTicket_AtomicOneWinner
- TestDeregisterShem
- ClaimTicket
- RegisterTicketRoutes
- ReviseClaim
- TestAvailableTickets
- TestClaimTicket_HTTPEndpoint
- TestUpdatePhase
- TestReviseClaim_Success
- TestReviseClaim_WrongShem_Conflict
- TestReviseClaim_WrongPhase_Conflict
- TestReviseClaim_HTTPEndpoint
- TestAppendLog

## Types

- Handlers
- ClaimResponse

## Imports

encoding/json, net/http, strconv, strings, time, github.com/leonp92/golem/internal/orchestrator/auth, github.com/leonp92/golem/internal/orchestrator/db, github.com/leonp92/golem/internal/orchestrator/sse, github.com/leonp92/golem/internal/orchestrator/ws, bytes, fmt, net/http/httptest, testing, github.com/gorilla/websocket, golang.org/x/crypto/bcrypt, github.com/leonp92/golem/internal/orchestrator/api, io, github.com/leonp92/golem/internal/orchestrator/urlnorm, gorm.io/gorm, sync
