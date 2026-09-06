package client

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/gorilla/websocket"
	ws "github.com/leonp92/golem/internal/orchestrator/ws"
)

// WSClient is a WebSocket client for the orchestrator push channel.
type WSClient struct {
	conn       *websocket.Conn
	apiKey     string
	pingTicker *time.Ticker
	done       chan struct{} // closed when Listen returns to stop the ping goroutine
}

// Connect establishes a WebSocket connection to the orchestrator.
func (wc *WSClient) Connect(url, apiKey, name string) error {
	hdr := http.Header{
		"Authorization": {"Bearer " + apiKey},
		"X-Shem-Name":   {name},
	}
	conn, _, err := websocket.DefaultDialer.Dial(url, hdr)
	if err != nil {
		return err
	}
	wc.conn = conn
	wc.apiKey = apiKey
	return nil
}

// Listen reads messages from the WebSocket and calls onMsg for each message.
// It runs until an error occurs (typically connection close).
func (wc *WSClient) Listen(onMsg func(ws.WSMessage)) error {
	// Start a goroutine to send heartbeat pings every 30 seconds.
	// Use a done channel so the goroutine exits cleanly when Listen returns;
	// Ticker.Stop does not close the channel, which would leak the goroutine.
	wc.done = make(chan struct{})
	wc.pingTicker = time.NewTicker(30 * time.Second)
	defer func() {
		close(wc.done)
		wc.pingTicker.Stop()
	}()

	go func() {
		for {
			select {
			case <-wc.pingTicker.C:
				_ = wc.SendPing()
			case <-wc.done:
				return
			}
		}
	}()

	for {
		_, data, err := wc.conn.ReadMessage()
		if err != nil {
			return err
		}

		var msg ws.WSMessage
		if err := json.Unmarshal(data, &msg); err != nil {
			continue
		}

		onMsg(msg)
	}
}

// SendPing sends a heartbeat ping to the orchestrator.
func (wc *WSClient) SendPing() error {
	data, _ := json.Marshal(map[string]string{"type": "ping"})
	return wc.conn.WriteMessage(websocket.TextMessage, data)
}
