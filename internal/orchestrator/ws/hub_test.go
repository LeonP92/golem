package ws_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	ws "github.com/leonp92/golem/internal/orchestrator/ws"
)

func TestHubPush(t *testing.T) {
	hub := ws.NewHub()

	var upgrader = websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }}
	// Dial returns once the 101 handshake is written, which can be before
	// the handler reaches Register; wait for it so Push doesn't race.
	registered := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("Upgrade: %v", err)
			return
		}
		hub.Register(1, conn)
		close(registered)
	}))
	defer srv.Close()

	url := "ws" + strings.TrimPrefix(srv.URL, "http")
	client, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer client.Close()

	select {
	case <-registered:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for hub.Register")
	}

	msg := ws.WSMessage{Type: "ticket_available", Repo: "https://github.com/org/repo"}
	if err := hub.Push(1, msg); err != nil {
		t.Fatalf("Push: %v", err)
	}
	_, data, _ := client.ReadMessage()
	var got ws.WSMessage
	json.Unmarshal(data, &got) //nolint:errcheck
	if got.Type != "ticket_available" {
		t.Errorf("got type %q", got.Type)
	}
}
