package api

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/gorilla/websocket"
	"github.com/leonp92/golem/internal/orchestrator/auth"
	"github.com/leonp92/golem/internal/orchestrator/db"
	"github.com/leonp92/golem/internal/orchestrator/sse"
	"github.com/leonp92/golem/internal/orchestrator/urlnorm"
	ws "github.com/leonp92/golem/internal/orchestrator/ws"
	"gorm.io/gorm"
)

// Handlers holds the shared dependencies for all API handlers.
type Handlers struct {
	DB            *gorm.DB
	Hub           *ws.Hub
	Broker        *sse.Broker
	LogEntryHTML  func(sse.LogEntryEvent) string // renders a log entry to HTML for SSE; nil = send JSON
}

// NewHandlers creates a Handlers with the given dependencies.
func NewHandlers(gdb *gorm.DB, hub *ws.Hub, broker *sse.Broker) *Handlers {
	return &Handlers{DB: gdb, Hub: hub, Broker: broker}
}

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true },
}

// RegisterShemRoutes adds shem-facing routes to mux.
func (h *Handlers) RegisterShemRoutes(mux *http.ServeMux) {
	mux.Handle("POST /api/shems/register", auth.RequireAPIKey(h.DB)(http.HandlerFunc(h.register)))
	mux.Handle("DELETE /api/shems/me", auth.RequireAPIKey(h.DB)(http.HandlerFunc(h.deregister)))
	mux.Handle("GET /api/shems", auth.RequireSession(h.DB)(http.HandlerFunc(h.listShems)))
	mux.Handle("GET /api/ws", auth.RequireAPIKey(h.DB)(http.HandlerFunc(h.wsUpgrade)))
}

func (h *Handlers) register(w http.ResponseWriter, r *http.Request) {
	shem := auth.ShemFromRequest(r)
	var body struct {
		Name  string   `json:"name"`
		Repos []string `json:"repos"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	normalized := make([]string, len(body.Repos))
	for i, repo := range body.Repos {
		normalized[i] = urlnorm.Normalize(repo)
	}
	reposJSON, _ := json.Marshal(normalized)
	now := time.Now()
	h.DB.Model(shem).Updates(map[string]any{
		"repos": string(reposJSON), "status": "online", "last_heartbeat": now,
	})
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"shem_id": shem.ID}) //nolint:errcheck
}

func (h *Handlers) deregister(w http.ResponseWriter, r *http.Request) {
	shem := auth.ShemFromRequest(r)
	h.DB.Model(shem).Updates(map[string]any{"status": "offline", "current_ticket": nil})
	h.Hub.Unregister(shem.ID)
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handlers) listShems(w http.ResponseWriter, r *http.Request) {
	var shems []db.Shem
	h.DB.Find(&shems)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(shems) //nolint:errcheck
}

func (h *Handlers) wsUpgrade(w http.ResponseWriter, r *http.Request) {
	shem := auth.ShemFromRequest(r)
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		http.Error(w, "upgrade failed", http.StatusBadRequest)
		return
	}
	// Retrieve repos for this shem from DB to register with hub.
	var repos []string
	json.Unmarshal([]byte(shem.Repos), &repos) //nolint:errcheck
	h.Hub.Register(shem.ID, conn, repos...)

	// Read-pump: read inbound frames (ping messages) and update last_heartbeat.
	// Unregisters the connection on any read error (disconnect or close).
	shemID := shem.ID
	go func() {
		defer h.Hub.Unregister(shemID)
		for {
			_, _, err := conn.ReadMessage()
			if err != nil {
				return
			}
			h.DB.Model(&db.Shem{}).Where("id = ?", shemID).Update("last_heartbeat", time.Now())
		}
	}()
}
