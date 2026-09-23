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
	// CSPMode controls the Content-Security-Policy header: "enforce"
	// (default), "report-only", or "off". Set from config.CSPConfig.Mode in
	// cmd/orchestrator/main.go, which validates it; an unset or otherwise
	// unrecognized value is treated as "enforce" by CSPMiddleware.
	CSPMode string
	// GitHubTokenEnv is the name of the environment variable the GitHub token
	// is read from, passed through to the handlers so a refusal can name the
	// variable this deployment actually uses.
	GitHubTokenEnv string

	// GitHubDefaultLabel is the trigger label a newly registered repository
	// starts with (config.GitHubConfig.TriggerLabel).
	GitHubDefaultLabel string
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
	h.GitHubTokenEnv = s.GitHubTokenEnv
	h.LogEntryHTML = ui.MakeLogEntryRenderer(tmpls)
	h.RegisterShemRoutes(mux)
	h.RegisterTicketRoutes(mux)
	h.RegisterLogRoutes(mux)
	h.RegisterHumanRoutes(mux)
	h.RegisterGitHubRoutes(mux)

	uiHandlers := ui.NewHandlersWithMap(s.DB, tmpls, s.SecureCookie)
	uiHandlers.GitHubDefaultLabel = s.GitHubDefaultLabel
	uiHandlers.Hub = s.Hub
	uiHandlers.RegisterRoutes(mux)

	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok")) //nolint:errcheck
	})

	// Content-Security-Policy. Inline scripts are allowed by hash, computed
	// from ui.TemplateFS() — the same //go:embed'd bytes the renderer above
	// parses — so the policy stays in sync as templates change and matches
	// what's actually served regardless of deployment (a disk path would be
	// wrong even where it exists: stale/locally-modified copies would yield
	// a mismatched policy, and it's simply absent in a container image that
	// ships only the compiled binary). If the hashes can't be computed,
	// don't ship a policy that silently blocks every inline script — log
	// loudly and disable the header entirely instead, regardless of the
	// configured mode.
	mode := s.CSPMode
	var policy string
	hashes, err := InlineScriptHashes(ui.TemplateFS())
	if err != nil {
		log.Printf("ERROR csp: InlineScriptHashes: %v — disabling Content-Security-Policy (mode=off) rather than shipping a stale or empty policy", err)
		mode = cspModeOff
	} else {
		policy = BuildPolicy(hashes)
	}
	return CSPMiddleware(policy, mode)(mux)
}
