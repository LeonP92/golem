package server

import (
	"log"
	"net/http"
	"time"

	"github.com/leonp92/golem/internal/orchestrator/api"
	"github.com/leonp92/golem/internal/orchestrator/sse"
	"github.com/leonp92/golem/internal/orchestrator/ui"
	ws "github.com/leonp92/golem/internal/orchestrator/ws"
	"gorm.io/gorm"
)

// Server holds the shared dependencies used by all HTTP handlers.
type Server struct {
	DB           *gorm.DB
	Hub          *ws.Hub
	Broker       *sse.Broker
	SecureCookie bool
	BaseURL      string
	// Sync triggers an immediate manual GitHub ingest pass. Left nil when
	// GitHub sync is not running (no repos enabled, or the token is
	// missing) — the default install; api.Handlers responds 503 in that
	// case rather than panicking. Set from cmd/orchestrator/main.go only
	// when the worker was actually started, so a nil *ghsync.Worker is
	// never boxed into this interface (see main.go for why that matters).
	Sync api.SyncTrigger
	// ManualSyncCooldown is the minimum gap between manual syncs of one repo.
	ManualSyncCooldown time.Duration
}

// New creates a Server with the given dependencies. baseURL is the
// orchestrator's externally reachable base URL, passed through to API
// handlers that link back to tickets (e.g. GitHub milestone comments).
func New(gdb *gorm.DB, hub *ws.Hub, broker *sse.Broker, secureCookie bool, baseURL string) *Server {
	return &Server{DB: gdb, Hub: hub, Broker: broker, SecureCookie: secureCookie, BaseURL: baseURL}
}

// Routes returns the full HTTP mux with all handler groups registered.
func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()

	// UI routes (human-facing HTML dashboard).
	tmpls, err := ui.LoadTemplates()
	if err != nil {
		log.Printf("warn: ui templates failed to load: %v — UI will return 500 on render", err)
	}

	// API routes (Shem-facing and session-protected JSON endpoints).
	h := api.NewHandlers(s.DB, s.Hub, s.Broker)
	h.BaseURL = s.BaseURL
	h.Sync = s.Sync
	h.ManualSyncCooldown = s.ManualSyncCooldown
	h.LogEntryHTML = ui.MakeLogEntryRenderer(tmpls)
	h.RegisterShemRoutes(mux)
	h.RegisterTicketRoutes(mux)
	h.RegisterLogRoutes(mux)
	h.RegisterHumanRoutes(mux)
	h.RegisterGitHubRoutes(mux)

	uiHandlers := ui.NewHandlersWithMap(s.DB, tmpls, s.SecureCookie)
	uiHandlers.RegisterRoutes(mux)

	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok")) //nolint:errcheck
	})
	return mux
}
