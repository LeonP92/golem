// Package ui provides HTTP handlers and HTML templates for the Golem
// Orchestrator web dashboard.
package ui

import (
	"bytes"
	"embed"
	"fmt"
	"html/template"
	"net/http"
	"time"

	"github.com/leonp92/golem/internal/orchestrator/auth"
	"github.com/leonp92/golem/internal/orchestrator/db"
	"github.com/leonp92/golem/internal/orchestrator/sse"
	"github.com/leonp92/golem/internal/orchestrator/urlnorm"
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
		"login":         "templates/login.html",
		"dashboard":     "templates/dashboard.html",
		"shems":         "templates/shems.html",
		"ticket_new":    "templates/ticket_new.html",
		"ticket_detail": "templates/ticket_detail.html",
	}

	out := make(map[string]*template.Template, len(pages))
	for name, pageFile := range pages {
		files := make([]string, 0, 2+len(partials))
		files = append(files, layoutFile, pageFile)
		files = append(files, partials...)
		t, err := template.New("").ParseFS(fs, files...)
		if err != nil {
			return nil, fmt.Errorf("parse %s: %w", name, err)
		}
		out[name] = t
	}
	return out, nil
}

// Handlers holds the shared dependencies for the UI HTTP handlers.
type Handlers struct {
	DB           *gorm.DB
	secureCookie bool
	tmpl         *template.Template // kept for backward compat; nil = use tmpls map
	tmpls        map[string]*template.Template
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
	mux.HandleFunc("GET /logout", h.logout)
	mux.Handle("GET /dashboard", auth.RequireSession(h.DB)(http.HandlerFunc(h.dashboard)))
	mux.Handle("GET /shems", auth.RequireSession(h.DB)(http.HandlerFunc(h.shems)))
	mux.Handle("GET /tickets/new", auth.RequireSession(h.DB)(http.HandlerFunc(h.ticketNewForm)))
	mux.Handle("POST /tickets/new", auth.RequireSession(h.DB)(http.HandlerFunc(h.ticketNewSubmit)))
	mux.Handle("GET /tickets/{id}", auth.RequireSession(h.DB)(http.HandlerFunc(h.ticketDetail)))
	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			http.Redirect(w, r, "/dashboard", http.StatusFound)
			return
		}
		http.NotFound(w, r)
	})
}

// render executes the named page template (wrapping in the layout).
func (h *Handlers) render(w http.ResponseWriter, page string, data any) {
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
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := t.ExecuteTemplate(w, "layout", data); err != nil {
		http.Error(w, "template error: "+err.Error(), http.StatusInternalServerError)
	}
}

// --- login ---

func (h *Handlers) loginPage(w http.ResponseWriter, r *http.Request) {
	h.render(w, "login", map[string]any{
		"Error": r.URL.Query().Get("error") != "",
	})
}

func (h *Handlers) loginSubmit(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Redirect(w, r, "/login?error=1", http.StatusFound)
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
	http.Redirect(w, r, "/dashboard", http.StatusFound)
}

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

	h.render(w, "dashboard", map[string]any{
		"Tickets": rows,
		"Nav":     "dashboard",
	})
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

	h.render(w, "shems", map[string]any{
		"Shems": rows,
		"Nav":   "shems",
	})
}

// --- ticket new ---

type ticketForm struct {
	RepoRemote  string
	BaseBranch  string
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

func (h *Handlers) ticketNewForm(w http.ResponseWriter, r *http.Request) {
	h.render(w, "ticket_new", map[string]any{
		"Form":           ticketForm{BaseBranch: "main"},
		"Error":          "",
		"AvailableRepos": h.shemsRepos(),
		"Nav":            "new",
	})
}

func (h *Handlers) ticketNewSubmit(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	form := ticketForm{
		RepoRemote:  r.FormValue("repo_remote"),
		BaseBranch:  r.FormValue("base_branch"),
		Description: r.FormValue("description"),
	}
	if form.RepoRemote == "" || form.BaseBranch == "" || form.Description == "" {
		h.render(w, "ticket_new", map[string]any{
			"Form":           form,
			"Error":          "All fields are required.",
			"AvailableRepos": h.shemsRepos(),
			"Nav":            "new",
		})
		return
	}
	user := auth.SessionUser(r)
	ticket := db.Ticket{
		RepoRemote:  urlnorm.Normalize(form.RepoRemote),
		Branch:      form.BaseBranch,
		Description: form.Description,
		Phase:       "unassigned",
	}
	if user != nil {
		ticket.CreatedByUserID = &user.ID
	}
	if err := h.DB.Create(&ticket).Error; err != nil {
		h.render(w, "ticket_new", map[string]any{
			"Form":           form,
			"Error":          "Failed to create ticket.",
			"AvailableRepos": h.shemsRepos(),
			"Nav":            "new",
		})
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

	var pending *db.HumanInput
	var hi db.HumanInput
	if h.DB.Where("ticket_id = ? AND resolved_at IS NULL AND kind != 'feedback'", rawID).
		Order("created_at asc").First(&hi).Error == nil {
		pending = &hi
	}

	h.render(w, "ticket_detail", map[string]any{
		"Ticket":        ticket,
		"CreatedByName": createdBy,
		"LogEntries":    logEntries,
		"PendingInput":  pending,
		"Spec":          spec,
		"Plan":          plan,
	})
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
