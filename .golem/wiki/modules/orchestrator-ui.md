# orchestrator/ui

HTTP handlers and HTML templates for the Golem Orchestrator human-facing web dashboard.

## What it does

Provides all browser-facing HTTP routes for the orchestrator:

- **Login/logout** — form-based session authentication using `auth.CreateSession` / `auth.RequireSession`
- **Dashboard** (`/dashboard`) — table of all tickets with phase badges and age, plus an "Action Required" panel listing all unresolved `HumanInput` rows across all tickets
- **Shems** (`/shems`) — table of registered Shem workers with online/offline status, last heartbeat, current ticket
- **Ticket new** (`GET /tickets/new`, `POST /tickets/new`) — form to create a ticket (repo remote URL, branch, description)
- **Ticket detail** (`/tickets/{id}`) — full view with metadata, action card for pending human input, log feed with HTMX SSE live-tail

## Key types

- `Handlers` — holds `*gorm.DB` and per-page `map[string]*template.Template`
- `TicketRow` — view-model for dashboard ticket rows (Ticket + ShemName + Age string)
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

## Integration

`server.Routes()` calls `ui.LoadTemplates()` and registers `uiHandlers.RegisterRoutes(mux)` alongside the API routes.

## Why

Provides human oversight of agent work: operators can monitor ticket progress, answer agent questions, approve plan/implement phase transitions, acknowledge blockers, and create new tickets without using the JSON API directly.
