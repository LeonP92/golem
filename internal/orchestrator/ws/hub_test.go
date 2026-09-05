package ws_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gorilla/websocket"
	ws "github.com/leonp92/golem/internal/orchestrator/ws"
)

func TestHubPush(t *testing.T) {
	hub := ws.NewHub()

	var upgrader = websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, _ := upgrader.Upgrade(w, r, nil)
		hub.Register(1, conn)
	}))
	defer srv.Close()

	url := "ws" + strings.TrimPrefix(srv.URL, "http")
	client, _, _ := websocket.DefaultDialer.Dial(url, nil)
	defer client.Close()

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
