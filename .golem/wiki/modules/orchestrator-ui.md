# orchestrator/ui

HTTP handlers and HTML templates for the Golem Orchestrator human-facing web dashboard.

## What it does

Provides all browser-facing HTTP routes for the orchestrator:

- **Login/logout** — form-based session authentication using `auth.CreateSession` / `auth.RequireSession`
- **Dashboard** (`/dashboard`) — table of all tickets with phase badges and age, plus an "Action Required" panel listing all unresolved `HumanInput` rows across all tickets
- **Shems** (`/shems`) — table of registered Shem workers with online/offline status, last heartbeat, current ticket
- **Ticket new** (`GET /tickets/new`, `POST /tickets/new`) — form to create a ticket (repo remote URL, branch, description)
- **Ticket detail** (`/tickets/{id}`) — full view with metadata, action card for pending human input, log feed with HTMX SSE live-tail. The close/complete action button's label/color/icon is conditional on `.Ticket.Phase`: `ready-for-review` shows green "Mark Complete"; any other non-terminal phase shows red "Close" — the `action:"close"` payload sent to the server is identical either way.

- **Users** (`GET /users`, `POST /users`, `POST /users/{id}/role`, `POST /users/{id}/delete`) — admin-only (`user:manage`) user management in `users.go`: list ordered by username, create (delegates to `admin.UsersAddOrUpdate` after an up-front uniqueness check; error branches route `admin.ErrWeakPassword` / `admin.ErrUnknownRole` to distinct messages), change role, delete. The add-user form is a compact row hidden inside the table until the "Add user" button is clicked (kept expanded when a validation error just re-rendered). The current user's row shows a `you` badge and the delete button is hidden — the backend also refuses self-delete, so this is UI defense in depth. Every refusal re-renders the page with an `Error` string and leaves the database untouched: duplicate username, invalid role, deleting your own account, and removing or demoting the last admin (`admin.ErrLastAdmin`). Deleting a user also drops their sessions, so their cookie stops working immediately.
- **Settings** (`GET /settings`, `GET /settings/security`, `POST /settings/security/password`) — available to any signed-in user, no permission gate (developers must be able to rotate their own password without needing `user:manage`). `GET /settings` 302s to the default section (`/settings/security`). The page is a sidebar-plus-content shell so new sections (profile, API keys, notifications) drop in as new routes + `{{define "settings_<name>"}}` blocks without touching the shell. Change-password requires the current password (blocks a hijacked session from silently rotating the credential) and confirms the new one; failures re-render inline with an `Error` banner, success with a `Success` banner.

## Authorization

Every authenticated route is registered through `h.sessionRoute(perm, fn)`, which nests `rbac.Require(perm)` inside `auth.RequireSession` — `ticket:view` for the dashboard and ticket detail, `shem:view` for `/shems`, `ticket:create` for the new-ticket form (see `orchestrator-rbac`). `/login`, `/logout` and `GET /` stay unauthenticated. The `/settings/*` routes wrap `auth.RequireSession` directly (no `rbac.Require`) since there is no permission gate.

`h.base(r, nav)` builds the render map every authenticated page starts from: `Nav`, `CurrentUser`, `CurrentUserID`, `CurrentRole`, `CanManageUsers`, `CanCreateTicket`. Handlers add their own keys to it rather than repeating these. `layout.html` uses the flags to hide controls the user cannot use (the "New Ticket" button, the "Users" nav item), and turns `username · role` into a link to `/settings`. Templates use `CurrentUserID` to hide destructive controls on the user's own row (e.g. delete on `/users`). The ticket detail page passes `nav=""`, which keeps `{{if .Nav}}` false and the page nav-less as before.

## Key types

- `Handlers` — holds `*gorm.DB` and per-page `map[string]*template.Template`
- `TicketRow` — view-model for dashboard ticket rows (Ticket + ShemName + CreatedByName + Age string)
- `ticketForm` — holds the new-ticket form fields for re-render on validation error

## Construction

```go
// Production: loads embedded templates from templates/ directory
tmpls, err := ui.LoadTemplates()
h := ui.NewHandlersWithMap(gdb, tmpls)
h.RegisterRoutes(mux)

// Tests that skip rendering: pass nil template
h := ui.NewHandlers(gdb, nil)
```

`LoadTemplates()` returns a `map[string]*template.Template` where each entry is a separate `*template.Template` instance containing: `layout.html` + the page file + all `partials/*.html`. This avoids the Go stdlib restriction on redefining template names within a single set.

## Template structure

- `templates/layout.html` — defines `{{define "layout"}}` with HTMX CDN, nav bar; calls `{{template "page_content" .}}`
- `templates/<page>.html` — defines `{{define "page_content"}}` for each page
- `templates/partials/log_entry.html` — `{{define "log_entry"}}` for a single log entry row (used in ticket_detail; SSE target)
- `templates/partials/action_card.html` — `{{define "action_card"}}` renders approve/answer/ack form via `hx-post`
- `templates/partials/ticket_row.html` — `{{define "ticket_row"}}` for dashboard table rows
- `templates/partials/shem_row.html` — `{{define "shem_row"}}` for shem table rows

`ticketNewSubmit` sets `Ticket.CreatedByUserID` from `auth.SessionUser(r)`. The dashboard table and ticket detail view both render a "Created by" column/field (via `db.CreatorNames`), showing "unknown" for legacy tickets with no recorded creator.

The ticket-new form requires a `Title` field (used server-side to compute the branch name via `internal/slug`). Since older tickets have `Title == ""` (no backfill), all render sites use the `displayTitle` template func (falls back to a truncated first line of `Description` when `Title` is empty) instead of showing `Title` raw, so pre-existing tickets keep a sensible heading.

## Integration

`server.Routes()` calls `ui.LoadTemplates()` and registers `uiHandlers.RegisterRoutes(mux)` alongside the API routes.

## Why

Provides human oversight of agent work: operators can monitor ticket progress, answer agent questions, approve plan/implement phase transitions, acknowledge blockers, and create new tickets without using the JSON API directly.
