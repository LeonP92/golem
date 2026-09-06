# internal/orchestrator/api

This module implements the HTTP API layer for the Golem orchestrator server, exposing REST endpoints consumed by both Shem agents (authenticated via API key) and human operators (authenticated via session cookie). It is organized into four route groups: ticket lifecycle management (create, list, get, claim, phase/checkpoint updates), Shem registration and WebSocket upgrade, log ingestion and SSE streaming, and human-input CRUD plus a unified ticket action dispatcher. The Handlers struct is the central dependency carrier holding a GORM database handle, a WebSocket hub for pushing real-time messages to connected Shems, and an SSE broker for streaming log events to browser clients. All mutation endpoints enforce ownership or phase preconditions before writing, and most state changes fan out notifications to relevant Shems via WebSocket broadcast or push.

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
- TestAvailableTickets
- TestClaimTicket_HTTPEndpoint
- TestUpdatePhase
- TestAppendLog

## Types

- Handlers
- ClaimResponse

## Imports

encoding/json, net/http, strconv, strings, time, github.com/leonp92/golem/internal/orchestrator/auth, github.com/leonp92/golem/internal/orchestrator/db, github.com/leonp92/golem/internal/orchestrator/sse, github.com/leonp92/golem/internal/orchestrator/ws, bytes, fmt, net/http/httptest, testing, golang.org/x/crypto/bcrypt, github.com/leonp92/golem/internal/orchestrator/api, io, github.com/gorilla/websocket, github.com/leonp92/golem/internal/orchestrator/urlnorm, gorm.io/gorm, sync
