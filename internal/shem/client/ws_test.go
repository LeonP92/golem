package client_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	ws "github.com/leonp92/golem/internal/orchestrator/ws"
	"github.com/leonp92/golem/internal/shem/client"
)

func TestWSClient_Connect(t *testing.T) {
	// Create a simple WebSocket server
	upgrader := websocket.Upgrader{
		CheckOrigin: func(r *http.Request) bool { return true },
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("upgrade failed: %v", err)
			return
		}
		defer conn.Close()

		// Read a message to keep connection open briefly
		_, _, _ = conn.ReadMessage()
	}))
	defer srv.Close()

	// Convert HTTP URL to WebSocket URL
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http")

	wc := &client.WSClient{}
	err := wc.Connect(wsURL, "test-key", "test-shem")
	if err != nil {
		t.Fatalf("Connect failed: %v", err)
	}
}

func TestWSClient_SendPing(t *testing.T) {
	upgrader := websocket.Upgrader{
		CheckOrigin: func(r *http.Request) bool { return true },
	}

	pingsReceived := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("upgrade failed: %v", err)
			return
		}
		defer conn.Close()

		// Read pings
		for i := 0; i < 2; i++ {
			_, data, err := conn.ReadMessage()
			if err != nil {
				break
			}
			var msg map[string]string
			if json.Unmarshal(data, &msg) == nil && msg["type"] == "ping" {
				pingsReceived++
			}
		}
	}))
	defer srv.Close()

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http")
	wc := &client.WSClient{}
	err := wc.Connect(wsURL, "test-key", "test-shem")
	if err != nil {
		t.Fatalf("Connect failed: %v", err)
	}

	err = wc.SendPing()
	if err != nil {
		t.Fatalf("SendPing failed: %v", err)
	}

	err = wc.SendPing()
	if err != nil {
		t.Fatalf("SendPing failed: %v", err)
	}
}

func TestWSClient_Listen(t *testing.T) {
	upgrader := websocket.Upgrader{
		CheckOrigin: func(r *http.Request) bool { return true },
	}

	const wantTicketID = "some-uuid-42"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("upgrade failed: %v", err)
			return
		}
		defer conn.Close()

		// Send a test message
		tid := wantTicketID
		msg := ws.WSMessage{
			Type:     "ticket_claimed",
			TicketID: &tid,
		}
		data, _ := json.Marshal(msg)
		conn.WriteMessage(websocket.TextMessage, data) //nolint:errcheck

		// Wait briefly then close
		time.Sleep(100 * time.Millisecond)
	}))
	defer srv.Close()

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http")
	wc := &client.WSClient{}
	err := wc.Connect(wsURL, "test-key", "test-shem")
	if err != nil {
		t.Fatalf("Connect failed: %v", err)
	}

	messagesReceived := 0
	go func() {
		// We expect Listen to return an error when the server closes
		err := wc.Listen(func(msg ws.WSMessage) {
			messagesReceived++
			if msg.Type != "ticket_claimed" {
				t.Errorf("expected ticket_claimed, got %s", msg.Type)
			}
			if msg.TicketID == nil || *msg.TicketID != wantTicketID {
				t.Errorf("expected ticket_id %q, got %v", wantTicketID, msg.TicketID)
			}
		})
		if err == nil {
			t.Error("expected Listen to return error on disconnect")
		}
	}()

	// Give time for Listen to receive the message
	time.Sleep(200 * time.Millisecond)

	if messagesReceived == 0 {
		t.Error("expected to receive at least one message")
	}
}
