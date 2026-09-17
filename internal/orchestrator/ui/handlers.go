// Package ui provides HTTP handlers and HTML templates for the Golem
// Orchestrator web dashboard.
package ui

import (
	"bytes"
	"embed"
	"fmt"
	"html/template"
	"io/fs"
	"log"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/leonp92/golem/internal/orchestrator/auth"
	"github.com/leonp92/golem/internal/orchestrator/db"
	"github.com/leonp92/golem/internal/orchestrator/ghsync"
	"github.com/leonp92/golem/internal/orchestrator/rbac"
	"github.com/leonp92/golem/internal/orchestrator/sse"
	"github.com/leonp92/golem/internal/orchestrator/urlnorm"
	"github.com/leonp92/golem/internal/slug"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
)

//go:embed templates
var embeddedFS embed.FS

// LoadTemplates parses all page and partial templates from the embedded FS,
// returning a map of page name -> *template.Template (each set includes the
// layout + the page + all partials).
func LoadTemplates() (map[string]*template.Template, error) {
	return loadTemplatesFromFS(embeddedFS)
}

// TemplateFS returns the embedded filesystem holding the UI template
// sources (paths like "templates/layout.html", "templates/partials/*.html")
// — the exact bytes LoadTemplates parses to serve the dashboard. Callers
// that need to reason about rendered template bytes without re-parsing them
// as templates — for example computing Content-Security-Policy script
// hashes — should read from this, not from a path on disk: a disk copy can
// be stale or locally modified, and a container image that ships only the
// compiled binary has no disk copy of the templates at all.
func TemplateFS() fs.FS {
	return embeddedFS
}

// MakeLogEntryRenderer returns a function that renders a LogEntryEvent to HTML
// using the log_entry partial from the template map. Used by the SSE handler to
// send rendered HTML instead of raw JSON, so the browser can append it directly.
func MakeLogEntryRenderer(tmpls map[string]*template.Template) func(sse.LogEntryEvent) string {
	t := tmpls["ticket_detail"]
	if t == nil {
		return nil
	}
	return func(evt sse.LogEntryEvent) string {
		var buf bytes.Buffer
		if err := t.ExecuteTemplate(&buf, "log_entry", evt); err != nil {
			return ""
		}
		return buf.String()
	}
}

var tmplFuncs = template.FuncMap{
	"firstLine": func(s string) string {
		for i, c := range s {
			if c == '\n' {
				return s[:i]
			}
		}
		return s
	},
	"truncate": func(n int, s string) string {
		runes := []rune(s)
		if len(runes) <= n {
			return s
		}
		return string(runes[:n]) + "…"
	},
	"displayTitle": func(title, description string) string {
		if title != "" {
			return title
		}
		runes := []rune(description)
		for i, c := range runes {
			if c == '\n' {
				runes = runes[:i]
				break
			}
		}
		if len(runes) > 80 {
			return string(runes[:80]) + "…"
		}
		return string(runes)
	},
}

func loadTemplatesFromFS(fs embed.FS) (map[string]*template.Template, error) {
	partialEntries, err := fs.ReadDir("templates/partials")
	if err != nil {
		return nil, fmt.Errorf("read partials: %w", err)
	}
	partials := make([]string, 0, len(partialEntries))
	for _, e := range partialEntries {
		if !e.IsDir() {
			partials = append(partials, "templates/partials/"+e.Name())
		}
	}

	const layoutFile = "templates/layout.html"
	pages := map[string]string{
		"login":           "templates/login.html",
		"dashboard":       "templates/dashboard.html",
		"shems":           "templates/shems.html",
		"github_settings": "templates/github_settings.html",
		"ticket_new":      "templates/ticket_new.html",
		"ticket_detail":   "templates/ticket_detail.html",
		"users":           "templates/users.html",
		"settings":        "templates/settings.html",
	}

	out := make(map[string]*template.Template, len(pages))
	for name, pageFile := range pages {
		files := make([]string, 0, 2+len(partials))
		files = append(files, layoutFile, pageFile)
		files = append(files, partials...)
		t, err := template.New("").Funcs(tmplFuncs).ParseFS(fs, files...)
		if err != nil {
			return nil, fmt.Errorf("parse %s: %w", name, err)
		}
		out[name] = t
	}
	return out, nil
}

// Handlers holds the shared dependencies for the UI HTTP handlers.
type Handlers struct {
	DB *gorm.DB

	// GitHubDefaultLabel seeds the trigger label for a repository that has
	// no saved settings yet, and is what the settings page offers for one a
	// shem declares but nobody has registered. Empty falls back to "golem",
	// so a Handlers built without it (every test that does not care) behaves
	// as before. Set from config.GitHubConfig.TriggerLabel.
	GitHubDefaultLabel string

	secureCookie bool
	tmpl         *template.Template // kept for backward compat; nil = use tmpls map
	tmpls        map[string]*template.Template
}

// defaultLabel returns the configured trigger-label default, or "golem".
func (h *Handlers) defaultLabel() string {
	if h.GitHubDefaultLabel != "" {
		return h.GitHubDefaultLabel
	}
	return ghsync.DefaultTriggerLabel
}

// NewHandlers creates a Handlers.  Pass tmpl=nil in tests that do not exercise
// template rendering (e.g. login redirect test); for a fully functional
// instance, build the template map via LoadTemplates and use NewHandlersWithMap.
func NewHandlers(gdb *gorm.DB, tmpl *template.Template) *Handlers {
	return &Handlers{DB: gdb, tmpl: tmpl}
}

// NewHandlersWithMap creates a Handlers using a pre-built page-template map
// (see LoadTemplates). Set secureCookie=true when the server is behind TLS.
func NewHandlersWithMap(gdb *gorm.DB, tmpls map[string]*template.Template, secureCookie bool) *Handlers {
	return &Handlers{DB: gdb, tmpls: tmpls, secureCookie: secureCookie}
}

// RegisterRoutes registers all UI routes on mux.
func (h *Handlers) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /login", h.loginPage)
	mux.HandleFunc("POST /login", h.loginSubmit)
	// POST, with the token (re-review finding F3). Logging out is a state
	// change, and as a GET it was reachable from the markdown sink: <img src>
	// survives the sanitizer by design, so `![](/logout)` in an issue body
	// logged the operator out on every page load of any page that rendered
	// it. RequireCSRF passes safe methods through, quite correctly — the bug
	// was that logout was not a safe method. No permission gate: signing out
	// is not a privilege.
	mux.Handle("POST /logout", h.authWriteRoute(h.logout))
	mux.Handle("GET /dashboard", h.sessionRoute(rbac.PermTicketView, h.dashboard))
	mux.Handle("GET /shems", h.sessionRoute(rbac.PermShemView, h.shems))
	// Viewing sync state is a developer concern; changing it is infrastructure
	// administration, so the writes sit behind PermShemManage (admin-only)
	// alongside the CSRF token every state-changing session route carries.
	mux.Handle("GET /settings/github", h.sessionRoute(rbac.PermShemView, h.githubSettings))
	mux.Handle("POST /settings/github", h.sessionWriteRoute(rbac.PermShemManage, h.githubSettingsSubmit))
	mux.Handle("POST /settings/github/outbox/{id}/retry",
		h.sessionWriteRoute(rbac.PermShemManage, h.retryParkedOutboxRow))
	mux.Handle("GET /tickets/new", h.sessionRoute(rbac.PermTicketCreate, h.ticketNewForm))
	mux.Handle("POST /tickets/new", h.sessionWriteRoute(rbac.PermTicketCreate, h.ticketNewSubmit))
	mux.Handle("GET /tickets/{id}", h.sessionRoute(rbac.PermTicketView, h.ticketDetail))
	mux.Handle("GET /users", h.sessionRoute(rbac.PermUserManage, h.usersPage))
	mux.Handle("POST /users", h.sessionWriteRoute(rbac.PermUserManage, h.usersCreate))
	mux.Handle("POST /users/{id}/role", h.sessionWriteRoute(rbac.PermUserManage, h.usersSetRole))
	mux.Handle("POST /users/{id}/delete", h.sessionWriteRoute(rbac.PermUserManage, h.usersDelete))
	// Self-service settings — no permission gate; adding a section is a
	// one-line route addition plus a {{define "settings_<name>"}} block.
	mux.Handle("GET /settings", h.authRoute(h.settingsIndex))
	mux.Handle("GET /settings/security", h.authRoute(h.settingsSecurity))
	mux.Handle("POST /settings/security/password", h.authWriteRoute(h.settingsChangePassword))
	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			http.Redirect(w, r, "/dashboard", http.StatusFound)
			return
		}
		http.NotFound(w, r)
	})
}

// sessionRoute wraps fn in session auth plus the permission check for p. Every
// permission-gated UI route goes through here, so the route table doubles as
// the audit list of who may call what.
func (h *Handlers) sessionRoute(p rbac.Permission, fn http.HandlerFunc) http.Handler {
	return auth.RequireSession(h.DB)(rbac.Require(p)(fn))
}

// authRoute is sessionRoute without a permission gate — use it for routes any
// signed-in user is allowed on (e.g. self-service settings). Keeps the route
// table the single audit surface for authz decisions.
func (h *Handlers) authRoute(fn http.HandlerFunc) http.Handler {
	return auth.RequireSession(h.DB)(fn)
}

// sessionWriteRoute is sessionRoute plus the CSRF token. Every state-changing
// session-authenticated route goes through here: an injected same-origin form
// in a markdown sink could otherwise make the operator's own browser perform
// the action (finding S1), and SameSite does nothing about same-origin.
func (h *Handlers) sessionWriteRoute(p rbac.Permission, fn http.HandlerFunc) http.Handler {
	return auth.RequireSession(h.DB)(auth.RequireCSRF(rbac.Require(p)(fn)))
}

// authWriteRoute is sessionWriteRoute without a permission gate, for state
// changes any signed-in user may make on their own account.
func (h *Handlers) authWriteRoute(fn http.HandlerFunc) http.Handler {
	return auth.RequireSession(h.DB)(auth.RequireCSRF(fn))
}

// base returns the render map every authenticated page starts from: the nav key
// plus the current user's identity and the permission flags the layout needs to
// decide which controls to show. Pass nav="" for pages that render without the
// nav bar (the ticket detail page).
func (h *Handlers) base(r *http.Request, nav string) map[string]any {
	u := auth.SessionUser(r)
	m := map[string]any{
		"Nav":             nav,
		"CurrentUser":     "",
		"CurrentUserID":   uint(0),
		"CurrentRole":     "",
		"CanManageUsers":  rbac.Can(u, rbac.PermUserManage),
		"CanCreateTicket": rbac.Can(u, rbac.PermTicketCreate),
	}
	if u != nil {
		m["CurrentUser"] = u.Username
		m["CurrentUserID"] = u.ID
		m["CurrentRole"] = u.Role
	}
	return m
}

// render executes the named page template (wrapping in the layout).
//
// It injects "CSRFToken" into the page data for every map-shaped payload, so
// the layout can publish it once (as a <meta>, which every htmx request then
// picks up) and individual forms can embed it as a hidden field, without each
// handler having to remember. A page rendered outside a session — /login —
// gets an empty token, which RequireCSRF rejects rather than accepts.
func (h *Handlers) render(w http.ResponseWriter, r *http.Request, page string, data any) {
	// Prefer the per-page map; fall back to the single legacy tmpl.
	var t *template.Template
	if h.tmpls != nil {
		t = h.tmpls[page]
	} else if h.tmpl != nil {
		t = h.tmpl
	}
	if t == nil {
		http.Error(w, "templates not loaded", http.StatusInternalServerError)
		return
	}
	if m, ok := data.(map[string]any); ok {
		m["CSRFToken"] = auth.CSRFToken(r)
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := t.ExecuteTemplate(w, "layout", data); err != nil {
		http.Error(w, "template error: "+err.Error(), http.StatusInternalServerError)
	}
}

// --- login ---

func (h *Handlers) loginPage(w http.ResponseWriter, r *http.Request) {
	// Minted here rather than by render's usual injection: render derives the
	// token from the SESSION, and the whole point of this page is that there
	// is not one yet. Rendered under its own key so the two can never be
	// confused for one another.
	token, err := auth.EnsureLoginCSRF(w, r, h.secureCookie)
	if err != nil {
		http.Error(w, "could not prepare the login form", http.StatusInternalServerError)
		return
	}
	h.render(w, r, "login", map[string]any{
		"Error":          r.URL.Query().Get("error") != "",
		"Expired":        r.URL.Query().Get("expired") != "",
		"LoginCSRFToken": token,
	})
}

func (h *Handlers) loginSubmit(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Redirect(w, r, "/login?error=1", http.StatusFound)
		return
	}
	// Checked before the credentials are even looked at. A cross-site POST
	// carrying the attacker's username and password would otherwise sign the
	// operator into the attacker's account, and everything they did next —
	// tickets created, issues approved — would happen somewhere the attacker
	// can read. Sending them back to a freshly rendered form rather than a
	// bare 403 is deliberate: the one person who hits this legitimately is a
	// user whose form sat open past the nonce's lifetime, and "try again" is
	// the correct instruction for them.
	if !auth.VerifyLoginCSRF(r) {
		http.Redirect(w, r, "/login?expired=1", http.StatusFound)
		return
	}
	username := r.FormValue("username")
	password := r.FormValue("password")

	var user db.User
	if err := h.DB.Where("username = ?", username).First(&user).Error; err != nil {
		http.Redirect(w, r, "/login?error=1", http.StatusFound)
		return
	}
	if bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(password)) != nil {
		http.Redirect(w, r, "/login?error=1", http.StatusFound)
		return
	}
	if err := auth.CreateSession(h.DB, w, user.ID, h.secureCookie); err != nil {
		http.Error(w, "session error", http.StatusInternalServerError)
		return
	}
	// The session-derived token governs from here; the pre-session nonce has
	// done its job and should not outlive it.
	auth.ClearLoginCSRF(w)
	http.Redirect(w, r, "/dashboard", http.StatusFound)
}

// logout clears the session cookie. It is registered for POST only and behind
// RequireCSRF — see RegisterRoutes.
func (h *Handlers) logout(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name:    "golem_session",
		Value:   "",
		Path:    "/",
		MaxAge:  -1,
		Expires: time.Unix(0, 0),
	})
	http.Redirect(w, r, "/login", http.StatusFound)
}

// --- dashboard ---

// TicketRow is the view-model passed to the ticket_row partial.
type TicketRow struct {
	Ticket             db.Ticket
	ShemName           string
	CreatedByName      string
	Age                string
	HasPendingApproval bool
}

type ShemRow struct {
	Shem          db.Shem
	ActiveTickets []db.Ticket
	RepoList      []string
}

func (h *Handlers) dashboard(w http.ResponseWriter, r *http.Request) {
	var tickets []db.Ticket
	h.DB.Order("created_at desc").Find(&tickets)

	var shems []db.Shem
	h.DB.Find(&shems)
	shemNames := make(map[uint]string, len(shems))
	for _, s := range shems {
		shemNames[s.ID] = s.Name
	}

	// Build set of ticket IDs with pending approvals.
	var approvalInputs []db.HumanInput
	h.DB.Where("resolved_at IS NULL AND kind = 'approval'").Find(&approvalInputs)
	pendingApprovalSet := make(map[string]bool, len(approvalInputs))
	for _, ai := range approvalInputs {
		pendingApprovalSet[ai.TicketID] = true
	}

	creatorNames := db.CreatorNames(h.DB, tickets)

	rows := make([]TicketRow, len(tickets))
	for i, t := range tickets {
		name := ""
		if t.AssignedShem != nil {
			name = shemNames[*t.AssignedShem]
		}
		createdBy := ""
		if t.CreatedByUserID != nil {
			createdBy = creatorNames[*t.CreatedByUserID]
		}
		rows[i] = TicketRow{
			Ticket:             t,
			ShemName:           name,
			CreatedByName:      createdBy,
			Age:                humanAge(t.CreatedAt),
			HasPendingApproval: pendingApprovalSet[t.ID],
		}
	}

	data := h.base(r, "dashboard")
	data["Tickets"] = rows
	h.render(w, r, "dashboard", data)
}

// --- shems ---

func (h *Handlers) shems(w http.ResponseWriter, r *http.Request) {
	var shems []db.Shem
	h.DB.Order("name asc").Find(&shems)

	var activeTickets []db.Ticket
	h.DB.Where("phase NOT IN ('closed','unassigned') AND assigned_shem IS NOT NULL").Find(&activeTickets)
	ticketsByShem := make(map[uint][]db.Ticket)
	for _, t := range activeTickets {
		ticketsByShem[*t.AssignedShem] = append(ticketsByShem[*t.AssignedShem], t)
	}

	rows := make([]ShemRow, len(shems))
	for i, s := range shems {
		rows[i] = ShemRow{
			Shem:          s,
			ActiveTickets: ticketsByShem[s.ID],
			RepoList:      s.RepoList(),
		}
	}

	data := h.base(r, "shems")
	data["Shems"] = rows
	h.render(w, r, "shems", data)
}

// --- ticket new ---

type ticketForm struct {
	RepoRemote  string
	BaseBranch  string
	Title       string
	Description string
}

// shemsRepos returns the deduplicated union of repos registered by all shems.
func (h *Handlers) shemsRepos() []string {
	var shems []db.Shem
	h.DB.Select("repos").Find(&shems)
	seen := make(map[string]struct{})
	var out []string
	for _, s := range shems {
		for _, r := range s.RepoList() {
			if _, ok := seen[r]; !ok {
				seen[r] = struct{}{}
				out = append(out, r)
			}
		}
	}
	return out
}

// renderTicketNew renders the new-ticket form with the given field values and
// an optional validation error.
func (h *Handlers) renderTicketNew(w http.ResponseWriter, r *http.Request, form ticketForm, errMsg string) {
	data := h.base(r, "new")
	data["Form"] = form
	data["Error"] = errMsg
	data["AvailableRepos"] = h.shemsRepos()
	h.render(w, r, "ticket_new", data)
}

func (h *Handlers) ticketNewForm(w http.ResponseWriter, r *http.Request) {
	h.renderTicketNew(w, r, ticketForm{BaseBranch: "main"}, "")
}

func (h *Handlers) ticketNewSubmit(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	form := ticketForm{
		RepoRemote:  r.FormValue("repo_remote"),
		BaseBranch:  r.FormValue("base_branch"),
		Title:       r.FormValue("title"),
		Description: r.FormValue("description"),
	}
	if form.RepoRemote == "" || form.BaseBranch == "" || form.Title == "" || form.Description == "" {
		h.renderTicketNew(w, r, form, "All fields are required.")
		return
	}
	user := auth.SessionUser(r)
	id := uuid.NewString()
	ticket := db.Ticket{
		ID:          id,
		RepoRemote:  urlnorm.Normalize(form.RepoRemote),
		BaseBranch:  form.BaseBranch,
		Title:       form.Title,
		Branch:      slug.Branch(form.Title, id),
		Description: form.Description,
		Phase:       "unassigned",
	}
	if user != nil {
		ticket.CreatedByUserID = &user.ID
	}
	if err := h.DB.Create(&ticket).Error; err != nil {
		h.renderTicketNew(w, r, form, "Failed to create ticket.")
		return
	}
	http.Redirect(w, r, fmt.Sprintf("/tickets/%s", ticket.ID), http.StatusFound)
}

// --- ticket detail ---

func (h *Handlers) ticketDetail(w http.ResponseWriter, r *http.Request) {
	rawID := r.PathValue("id")
	if rawID == "" {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}

	var ticket db.Ticket
	if err := h.DB.First(&ticket, "id = ?", rawID).Error; err != nil {
		http.Error(w, "ticket not found", http.StatusNotFound)
		return
	}
	createdBy := ""
	if ticket.CreatedByUserID != nil {
		names := db.CreatorNames(h.DB, []db.Ticket{ticket})
		createdBy = names[*ticket.CreatedByUserID]
	}

	var allEntries []db.LogEntry
	h.DB.Where("ticket_id = ?", rawID).Order("sequence_num asc").Find(&allEntries)
	if allEntries == nil {
		allEntries = []db.LogEntry{}
	}

	// Separate SPEC/PLAN documents from the regular log stream.
	// Both are shown in dedicated collapsible sections; the last one of each type wins.
	var spec, plan *db.LogEntry
	logEntries := make([]db.LogEntry, 0, len(allEntries))
	for i := range allEntries {
		switch allEntries[i].EntryType {
		case "SPEC":
			spec = &allEntries[i]
		case "PLAN":
			plan = &allEntries[i]
		default:
			logEntries = append(logEntries, allEntries[i])
		}
	}

	// Newest first for display. The scan above stays ASCENDING on purpose —
	// "the last one of each type wins" for SPEC and PLAN depends on that
	// order — so the reversal happens here, on the display slice only.
	for i, j := 0, len(logEntries)-1; i < j; i, j = i+1, j-1 {
		logEntries[i], logEntries[j] = logEntries[j], logEntries[i]
	}

	// Find rather than First: most tickets have no pending question, and
	// First logs "record not found" at error level for every one of them —
	// on a page that polls, so the log fills with a red line describing the
	// normal case. Same reason as ReposRemove and ensureAdmin.
	var pending *db.HumanInput
	var found []db.HumanInput
	if err := h.DB.Where("ticket_id = ? AND resolved_at IS NULL AND kind != 'feedback'", rawID).
		Order("created_at asc").Limit(1).Find(&found).Error; err != nil {
		log.Printf("ui: load pending human input for %s: %v", rawID, err)
	} else if len(found) > 0 {
		pending = &found[0]
	}

	// nav="" keeps the ticket detail page's current nav-less layout.
	data := h.base(r, "")
	data["Ticket"] = ticket
	data["CreatedByName"] = createdBy
	data["LogEntries"] = logEntries
	data["PendingInput"] = pending
	data["Spec"] = spec
	data["Plan"] = plan
	h.render(w, r, "ticket_detail", data)
}

// humanAge returns a short human-readable age string for the given time.
func humanAge(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}
