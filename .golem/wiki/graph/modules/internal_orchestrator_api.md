# internal/orchestrator/api



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
- TestCreateTicket_SetsCreatedByFromSession
- TestCreateTicket_IgnoresClientSuppliedCreatedByUserID
- TestListAndGetTicket_ReturnsCreatedBy
- TestCreateTicket_RequiresTitle
- TestCreateTicket_ComputesBranchFromTitle
- TestSessionRoutes_UnknownRoleForbidden

## Types

- Handlers
- ClaimResponse

## Imports

encoding/json, net/http, strconv, strings, time, github.com/leonp92/golem/internal/orchestrator/auth, github.com/leonp92/golem/internal/orchestrator/db, github.com/leonp92/golem/internal/orchestrator/rbac, github.com/leonp92/golem/internal/orchestrator/sse, github.com/leonp92/golem/internal/orchestrator/ws, bytes, fmt, net/http/httptest, testing, github.com/gorilla/websocket, golang.org/x/crypto/bcrypt, github.com/leonp92/golem/internal/orchestrator/api, io, github.com/leonp92/golem/internal/orchestrator/urlnorm, gorm.io/gorm, sync, github.com/google/uuid, github.com/leonp92/golem/internal/slug
