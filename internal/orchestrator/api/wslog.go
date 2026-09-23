package api

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/gorilla/websocket"
)

const (
	// wsWriteWait bounds a single write so one wedged reader cannot hold the
	// handler goroutine — and its broker subscription — open indefinitely.
	wsWriteWait = 10 * time.Second
	// wsPongWait is how long to wait for a pong before declaring the peer
	// gone. A laptop that sleeps or a network that drops leaves an
	// established socket that never errors on its own; without this the
	// subscription survives until the process restarts.
	wsPongWait = 60 * time.Second
	// wsPingPeriod must be meaningfully less than wsPongWait so a single
	// dropped ping does not by itself close a healthy connection.
	wsPingPeriod = (wsPongWait * 9) / 10
)

// wsLogUpgrader is deliberately NOT the package-level `upgrader` the shem
// endpoint uses.
//
// That one sets CheckOrigin to return true unconditionally, which is correct
// there: /api/ws authenticates with a bearer API key, a credential no browser
// holds and none will attach on its own, so a hostile page cannot forge an
// authenticated handshake no matter what Origin it sends.
//
// This endpoint authenticates with the operator's SESSION COOKIE, and the
// same-origin policy does not apply to WebSockets — a browser happily opens
// ws:// to another origin and attaches that origin's cookies. Reusing the
// permissive upgrader would therefore mean any page the operator visits while
// logged in could open this socket and stream the ticket's entire activity
// log: repository contents, file paths and agent output. That is
// cross-site WebSocket hijacking, and the Origin check is the defence.
var wsLogUpgrader = websocket.Upgrader{CheckOrigin: sameOriginWS}

// sameOriginWS reports whether the handshake came from this same origin.
//
// A missing Origin is refused rather than allowed. Browsers always send one
// on a WebSocket handshake, so absence means the caller is not the browser
// this cookie was issued to — and it is exactly what an attacker would omit
// if omission were treated as trustworthy. Non-browser clients have the
// API-key endpoint.
func sameOriginWS(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return false
	}
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" {
		return false
	}
	return strings.EqualFold(u.Host, r.Host)
}

// wsLog streams a ticket's log entries to the browser over a WebSocket.
//
// It replaces the Server-Sent Events endpoint that did the same job. SSE runs
// over a plain HTTP request, and browsers cap HTTP/1.1 at six connections per
// origin; the activity log held one open for as long as a ticket tab was
// open, so six tabs consumed the entire pool and every subsequent request to
// the orchestrator — including an ordinary page navigation — queued behind
// them and never completed. The UI simply stopped loading, while curl against
// the same server stayed instant because the limit is the browser's, not the
// server's. Chromium pools WebSockets separately and allows 255 per host, so
// the feed no longer competes with page loads.
//
// (HTTP/2 would also have solved it by multiplexing, but only over TLS, which
// the local stack does not run.)
func (h *Handlers) wsLog(w http.ResponseWriter, r *http.Request) {
	id, err := parseIDFromPath(r)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}

	// Subscribe BEFORE upgrading. Upgrade hijacks the connection, so a
	// failure after it can no longer be reported as an HTTP status; more
	// importantly, subscribing first means no entry published between the
	// handshake completing and the loop starting is missed.
	ch, cancel := h.Broker.Subscribe(id)
	defer cancel()

	conn, err := wsLogUpgrader.Upgrade(w, r, nil)
	if err != nil {
		// Upgrade has already written the error response.
		return
	}
	defer conn.Close() //nolint:errcheck

	// The client sends nothing, but a reader is still required: gorilla
	// processes close and pong frames only from within ReadMessage, so
	// without this goroutine a closed tab would never be noticed and the
	// subscription would leak.
	closed := make(chan struct{})
	go func() {
		defer close(closed)
		conn.SetReadLimit(512)
		_ = conn.SetReadDeadline(time.Now().Add(wsPongWait))
		conn.SetPongHandler(func(string) error {
			return conn.SetReadDeadline(time.Now().Add(wsPongWait))
		})
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	}()

	ping := time.NewTicker(wsPingPeriod)
	defer ping.Stop()

	for {
		select {
		case evt, open := <-ch:
			if !open {
				return
			}
			var payload string
			if h.LogEntryHTML != nil {
				payload = h.LogEntryHTML(evt)
			} else {
				data, _ := json.Marshal(evt)
				payload = string(data)
			}
			_ = conn.SetWriteDeadline(time.Now().Add(wsWriteWait))
			if err := conn.WriteMessage(websocket.TextMessage, []byte(payload)); err != nil {
				return
			}
		case <-ping.C:
			_ = conn.SetWriteDeadline(time.Now().Add(wsWriteWait))
			if err := conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		case <-closed:
			return
		case <-r.Context().Done():
			return
		}
	}
}
