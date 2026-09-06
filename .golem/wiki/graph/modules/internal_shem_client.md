# internal/shem/client

This module provides the HTTP and WebSocket client layer that a shem (worker node) uses to communicate with the Golem orchestrator server. The HTTP client wraps all REST API calls behind a retry-on-5xx policy with exponential back-off, covering shem registration/deregistration, ticket claiming, phase and checkpoint updates, structured log posting, document file streaming, and human-input polling and acknowledgement. The WebSocket client establishes a push channel to receive real-time orchestrator events, maintains connection liveness via periodic heartbeat pings, and dispatches decoded messages to a caller-supplied handler.

## Functions

- New
- Register
- Deregister
- ClaimTicket
- PostPhase
- PostCheckpoint
- PostLog
- PostDocumentFile
- GetPendingInput
- PostApprovalRequest
- GetPendingApproval
- GetPendingFeedback
- AckInput
- GetAvailable
- TestClient_RetriesOn5xx
- TestClient_AuthorizationHeader
- TestClient_ExhaustsRetries
- TestClient_Register
- TestClient_ClaimTicket_409
- TestClient_GetAvailable
- TestClient_GetAvailable_Empty
- Connect
- Listen
- SendPing
- TestWSClient_Connect
- TestWSClient_SendPing
- TestWSClient_Listen

## Types

- LogPayload
- ClaimResponse
- PendingInput
- Client
- WSClient

## Imports

bytes, encoding/json, errors, fmt, io, net/http, os, time, github.com/leonp92/golem/internal/orchestrator/db, net/http/httptest, testing, github.com/leonp92/golem/internal/shem/client, github.com/gorilla/websocket, github.com/leonp92/golem/internal/orchestrator/ws, strings
