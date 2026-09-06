# internal/orchestrator/auth

This module provides two independent HTTP authentication mechanisms for the Golem orchestrator: API key authentication for machine-to-machine shem node requests (Bearer token validated against bcrypt hashes stored in the database), and session-based authentication for human users accessing the web UI (random token stored as SHA-256 hash in the database with a 7-day TTL, delivered via HTTP-only cookie). Both mechanisms inject their authenticated principal into the request context for downstream handlers to retrieve.

## Functions

- RequireAPIKey
- ShemFromRequest
- TestAPIKeyAuth
- TestAPIKeyAuth_Unauthorized
- CreateSession
- RequireSession
- SessionUser
- TestSessionRoundTrip
- TestRequireSession_RedirectsWithoutCookie

## Imports

context, net/http, strings, golang.org/x/crypto/bcrypt, github.com/leonp92/golem/internal/orchestrator/db, gorm.io/gorm, net/http/httptest, testing, github.com/leonp92/golem/internal/orchestrator/auth, crypto/rand, crypto/sha256, encoding/hex, time
