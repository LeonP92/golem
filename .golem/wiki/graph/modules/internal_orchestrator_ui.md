# internal/orchestrator/ui

This module provides the HTTP handler layer and embedded HTML template engine for the Golem Orchestrator web dashboard. It parses all page and partial templates from an embedded FS into a page-keyed map at startup (layout + page + partials, with helper template funcs for truncation and display titles) and exposes handlers for login/logout, the ticket dashboard, shem listing, ticket creation, ticket detail, and admin user management. Every authenticated route is registered through a single sessionRoute helper that composes session authentication with an RBAC permission check, so the route table doubles as the authorization audit list; the shared base render map carries the current user's identity plus permission flags so the layout can hide controls the user may not use. The ticket detail view separates SPEC and PLAN documents from the regular log stream and surfaces any pending human-input request, the user-management handlers delegate to the admin package and translate its last-admin errors (plus a self-delete refusal) into re-rendered form validation messages, and an SSE-compatible renderer converts structured log events into HTML fragments for live streaming to the browser.

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
- TestTicketNewSubmit_SetsCreatedByFromSession
- TestTicketNewSubmit_MissingTitleRerendersForm
- TestLoadTemplates
- TestUsersRoutes_DeveloperForbidden
- TestDeveloperCanReachTicketRoutes
- TestAdminCreatesUser_EitherRole
- TestUsersPage_DeveloperSeesNoUsersNav
- TestUsersCreate_Validation
- TestUsersDelete_RefusesSelf
- TestUsersDelete_RefusesLastAdmin
- TestUsersDelete_UnknownUser
- TestUsersSetRole_RefusesDemotingLastAdmin
- TestUsersDelete_RevokesSessions
- TestRolePromotionTakesEffectImmediately

## Types

- Handlers
- TicketRow
- ShemRow

## Imports

bytes, embed, fmt, html/template, net/http, time, github.com/google/uuid, github.com/leonp92/golem/internal/orchestrator/auth, github.com/leonp92/golem/internal/orchestrator/db, github.com/leonp92/golem/internal/orchestrator/rbac, github.com/leonp92/golem/internal/orchestrator/sse, github.com/leonp92/golem/internal/orchestrator/urlnorm, github.com/leonp92/golem/internal/slug, golang.org/x/crypto/bcrypt, gorm.io/gorm, net/http/httptest, strings, testing, github.com/leonp92/golem/internal/orchestrator/ui, errors, strconv, github.com/leonp92/golem/internal/orchestrator/admin
