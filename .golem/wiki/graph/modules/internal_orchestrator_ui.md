# internal/orchestrator/ui

This module provides the HTTP handler layer and HTML template engine for the Golem Orchestrator web dashboard. It embeds all templates in the binary via go:embed, parses them into a page-keyed map at startup, and exposes route handlers for login/logout, the ticket dashboard, shem listing, ticket creation, and ticket detail views. It enforces session authentication via middleware on protected routes, renders server-side HTML using a layout-plus-page-plus-partials template composition model, and provides an SSE-compatible log entry renderer that converts structured log events to HTML fragments for live streaming to the browser.

## Functions

- LoadTemplates
- MakeLogEntryRenderer
- NewHandlers
- NewHandlersWithMap
- RegisterRoutes
- TestLoginHandler_ValidCredentials
- TestLoginHandler_InvalidCredentials
- TestLoginHandler_UnknownUser
- TestDashboard_RequiresSession
- TestLoadTemplates

## Types

- Handlers
- TicketRow

## Imports

bytes, embed, encoding/json, fmt, html/template, net/http, time, github.com/leonp92/golem/internal/orchestrator/auth, github.com/leonp92/golem/internal/orchestrator/db, github.com/leonp92/golem/internal/orchestrator/sse, github.com/leonp92/golem/internal/orchestrator/urlnorm, golang.org/x/crypto/bcrypt, gorm.io/gorm, net/http/httptest, strings, testing, github.com/leonp92/golem/internal/orchestrator/ui
