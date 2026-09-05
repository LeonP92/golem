package ws

import (
	"encoding/json"
	"fmt"
	"sync"

	"github.com/gorilla/websocket"
)

// WSMessage is the JSON payload sent over WebSocket connections.
type WSMessage struct {
	Type     string  `json:"type"`
	TicketID *string `json:"ticket_id,omitempty"`
	Repo     string  `json:"repo,omitempty"`
	InputID  *uint   `json:"input_id,omitempty"`
	Response string  `json:"response,omitempty"`
}

type connEntry struct {
	conn  *websocket.Conn
	mu    sync.Mutex // guards conn.WriteMessage; gorilla/websocket allows one concurrent writer
	repos []string   // normalized repo remote URLs this shem declared
}

// Hub maintains the set of active WebSocket connections keyed by shem ID.
type Hub struct {
	mu    sync.RWMutex
	conns map[uint]*connEntry
}

// NewHub creates an empty Hub.
func NewHub() *Hub { return &Hub{conns: make(map[uint]*connEntry)} }

// Register associates a WebSocket connection with a shem ID and its declared repos.
func (h *Hub) Register(shemID uint, conn *websocket.Conn, repos ...string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.conns[shemID] = &connEntry{conn: conn, repos: repos}
}

// Unregister closes and removes the connection for a shem ID.
func (h *Hub) Unregister(shemID uint) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if e, ok := h.conns[shemID]; ok {
		e.conn.Close()
		delete(h.conns, shemID)
	}
}

// Push sends a message to the shem with the given ID.
// Returns an error if the shem is not connected.
func (h *Hub) Push(shemID uint, msg WSMessage) error {
	h.mu.RLock()
	e, ok := h.conns[shemID]
	h.mu.RUnlock()
	if !ok {
		return fmt.Errorf("shem %d not connected", shemID)
	}
	data, _ := json.Marshal(msg)
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.conn.WriteMessage(websocket.TextMessage, data)
}

// Broadcast sends a message to all shems that have declared the given repo remote URL.
func (h *Hub) Broadcast(repoRemote string, msg WSMessage) {
	h.mu.RLock()
	entries := make([]*connEntry, 0)
	for _, e := range h.conns {
		for _, r := range e.repos {
			if r == repoRemote {
				entries = append(entries, e)
				break
			}
		}
	}
	h.mu.RUnlock()

	data, _ := json.Marshal(msg)
	for _, e := range entries {
		e.mu.Lock()
		e.conn.WriteMessage(websocket.TextMessage, data) //nolint:errcheck
		e.mu.Unlock()
	}
}
