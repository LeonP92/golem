package ws_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	ws "github.com/leonp92/golem/internal/orchestrator/ws"
)

// The read loop for a shem's OLD connection must not tear down its NEW one.
//
// When the reaper unregisters a shem it closes the connection; the shem
// reconnects and registers a new one. The old connection's read loop then
// exits and unregistered by shem ID alone, closing the new connection too —
// leaving the shem heartbeating as online while every push to it failed.
func TestHub_UnregisterConnLeavesANewerConnection(t *testing.T) {
	hub := ws.NewHub()
	upgrader := websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }}
	conns := make(chan *websocket.Conn, 2)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("Upgrade: %v", err)
			return
		}
		conns <- conn
	}))
	defer srv.Close()

	url := "ws" + strings.TrimPrefix(srv.URL, "http")
	dial := func() (*websocket.Conn, *websocket.Conn) {
		client, _, err := websocket.DefaultDialer.Dial(url, nil)
		if err != nil {
			t.Fatalf("Dial: %v", err)
		}
		select {
		case server := <-conns:
			return client, server
		case <-time.After(5 * time.Second):
			t.Fatal("timed out waiting for upgrade")
		}
		return nil, nil
	}

	oldClient, oldServer := dial()
	defer oldClient.Close()
	hub.Register(1, oldServer)
	newClient, newServer := dial()
	defer newClient.Close()
	hub.Register(1, newServer)

	hub.UnregisterConn(1, oldServer)

	if err := hub.Push(1, ws.WSMessage{Type: "ticket_available"}); err != nil {
		t.Fatalf("Push after stale unregister: %v", err)
	}
	_ = newClient.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, _, err := newClient.ReadMessage(); err != nil {
		t.Errorf("new connection did not receive the push: %v", err)
	}
}
