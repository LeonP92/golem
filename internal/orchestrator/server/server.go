package server

import (
	"log"
	"net/http"

	"github.com/leonp92/golem/internal/orchestrator/api"
	"github.com/leonp92/golem/internal/orchestrator/sse"
	"github.com/leonp92/golem/internal/orchestrator/ui"
	ws "github.com/leonp92/golem/internal/orchestrator/ws"
	"gorm.io/gorm"
)

// Server holds the shared dependencies used by all HTTP handlers.
type Server struct {
	DB     *gorm.DB
	Hub    *ws.Hub
	Broker *sse.Broker
}

// New creates a Server with the given dependencies.
func New(gdb *gorm.DB, hub *ws.Hub, broker *sse.Broker) *Server {
	return &Server{DB: gdb, Hub: hub, Broker: broker}
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
	h.LogEntryHTML = ui.MakeLogEntryRenderer(tmpls)
	h.RegisterShemRoutes(mux)
	h.RegisterTicketRoutes(mux)
	h.RegisterLogRoutes(mux)
	h.RegisterHumanRoutes(mux)

	uiHandlers := ui.NewHandlersWithMap(s.DB, tmpls)
	uiHandlers.RegisterRoutes(mux)

	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok")) //nolint:errcheck
	})
	return mux
}
