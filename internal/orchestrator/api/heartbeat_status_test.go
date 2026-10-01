package api_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/leonp92/golem/internal/orchestrator/db"
)

// Heartbeats must bring a shem back online. The reaper marks a shem offline
// and only registration set it online again, so a shem that stayed up and
// kept its websocket heartbeating showed as offline indefinitely.
func TestWSHeartbeat_MarksShemOnline(t *testing.T) {
	h, mux := setupTicketTest(t)
	shem := seedShem(t, h, "shem-a", "key-a") // seeded offline
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	hdr := http.Header{}
	hdr.Set("Authorization", "Bearer key-a")
	hdr.Set("X-Shem-Name", "shem-a")
	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http")+"/api/ws", hdr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	if err := conn.WriteMessage(websocket.TextMessage, []byte("ping")); err != nil {
		t.Fatalf("write: %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		var got db.Shem
		h.DB.First(&got, shem.ID)
		if got.Status == "online" {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Error("shem still offline after a heartbeat")
}
