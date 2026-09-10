# internal/orchestrator/auth

This module provides two independent HTTP authentication mechanisms for the Golem orchestrator: API key authentication for machine-to-machine shem node requests, validating an "X-Shem-Name" header plus a Bearer token against a bcrypt hash stored on the db.Shem record, and session-based authentication for human web UI users, issuing a random 32-byte token whose SHA-256 hash is stored in db.Session with a 7-day TTL and delivered via an HTTP-only "golem_session" cookie. Both mechanisms are implemented as net/http middleware that inject their authenticated principal (a *db.Shem or *db.User) into the request context via unexported context keys, with accessor functions to retrieve the principal in downstream handlers; API key failures return 401 while session failures redirect to /login.

## Functions

- RequireAPIKey
- ShemFromRequest
- TestAPIKeyAuth
- TestAPIKeyAuth_Unauthorized
- TestAPIKeyAuth_MissingName
- CreateSession
- RequireSession
- SessionUser
- TestSessionRoundTrip
- TestRequireSession_RedirectsWithoutCookie

## Imports

context, net/http, strings, golang.org/x/crypto/bcrypt, github.com/leonp92/golem/internal/orchestrator/db, gorm.io/gorm, net/http/httptest, testing, github.com/leonp92/golem/internal/orchestrator/auth, crypto/rand, crypto/sha256, encoding/hex, time
