package api_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/leonp92/golem/internal/orchestrator/api"
	"github.com/leonp92/golem/internal/orchestrator/auth"
	"github.com/leonp92/golem/internal/orchestrator/db"
	"github.com/leonp92/golem/internal/orchestrator/rbac"
	"github.com/leonp92/golem/internal/orchestrator/sse"
	ws "github.com/leonp92/golem/internal/orchestrator/ws"
	"gorm.io/gorm"
)

// setupWSLogTest returns a live server, the broker feeding it, a ticket ID and
// a session cookie for an admin.
func setupWSLogTest(t *testing.T) (*httptest.Server, *sse.Broker, string, *http.Cookie) {
	t.Helper()
	gdb, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	ticket := db.Ticket{RepoRemote: "r", Branch: "b", Description: "d", Phase: "implement"}
	if err := gdb.Create(&ticket).Error; err != nil {
		t.Fatalf("create ticket: %v", err)
	}
	broker := sse.NewBroker()
	h := api.NewHandlers(gdb, ws.NewHub(), broker)
	h.LogEntryHTML = func(e sse.LogEntryEvent) string {
		return "<div class=\"entry\">" + e.Message + "</div>"
	}
	mux := http.NewServeMux()
	h.RegisterLogRoutes(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, broker, ticket.ID, adminCookie(t, gdb)
}

func adminCookie(t *testing.T, gdb *gorm.DB) *http.Cookie {
	t.Helper()
	user := db.User{Username: "wsadmin", PasswordHash: "x", Role: string(rbac.RoleAdmin)}
	if err := gdb.Create(&user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	rec := httptest.NewRecorder()
	if err := auth.CreateSession(gdb, rec, user.ID, false); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	return rec.Result().Cookies()[0]
}

// wsURL converts an httptest http:// base URL to the ws:// log endpoint.
func wsURL(base, ticketID string) string {
	return "ws" + strings.TrimPrefix(base, "http") + "/ws/tickets/" + ticketID + "/log"
}

// dial connects with the given cookie and Origin. An empty origin sends none.
func dial(t *testing.T, srv *httptest.Server, ticketID string, cookie *http.Cookie, origin string) (*websocket.Conn, *http.Response, error) {
	t.Helper()
	hdr := http.Header{}
	if cookie != nil {
		hdr.Set("Cookie", cookie.Name+"="+cookie.Value)
	}
	if origin != "" {
		hdr.Set("Origin", origin)
	}
	d := websocket.Dialer{HandshakeTimeout: 5 * time.Second}
	return d.Dial(wsURL(srv.URL, ticketID), hdr) //nolint:bodyclose
}

// TestWSLog_StreamsPublishedEntries is the core of the SSE→WebSocket move.
//
// Browsers cap HTTP/1.1 at 6 connections per origin, and the activity log held
// one open per ticket tab, so six tabs wedged the entire UI — every later
// request, including a plain navigation, queued behind them forever. Chromium
// pools WebSockets separately (255 per host), so the same feed over a socket
// does not compete with page loads at all.
func TestWSLog_StreamsPublishedEntries(t *testing.T) {
	srv, broker, ticketID, cookie := setupWSLogTest(t)

	conn, _, err := dial(t, srv, ticketID, cookie, srv.URL)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	// Subscribe happens during the handshake, but the handler reaches
	// Subscribe slightly after Dial returns; wait for it so the publish
	// below cannot race ahead of the subscription.
	waitForSubscribers(t, broker, ticketID, 1)

	broker.Publish(ticketID, sse.LogEntryEvent{SequenceNum: 1, EntryType: "STATUS", Message: "hello"})

	if err := conn.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatalf("SetReadDeadline: %v", err)
	}
	typ, data, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("ReadMessage: %v", err)
	}
	if typ != websocket.TextMessage {
		t.Errorf("message type = %d, want TextMessage", typ)
	}
	// The payload must be the rendered HTML the feed prepends, not JSON —
	// the client inserts it directly.
	if !strings.Contains(string(data), "hello") || !strings.Contains(string(data), "<div") {
		t.Errorf("payload = %q, want the rendered log-entry HTML", data)
	}
}

// TestWSLog_RequiresSession: the endpoint is session-authenticated like the
// SSE one it replaces. An upgrade must not be a way around that.
func TestWSLog_RequiresSession(t *testing.T) {
	srv, _, ticketID, _ := setupWSLogTest(t)

	conn, resp, err := dial(t, srv, ticketID, nil, srv.URL)
	if err == nil {
		conn.Close()
		t.Fatal("unauthenticated client completed the WebSocket handshake")
	}
	if resp == nil {
		t.Fatalf("no HTTP response: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized && resp.StatusCode != http.StatusFound {
		t.Errorf("status = %d, want 401 or a redirect to login", resp.StatusCode)
	}
}

// TestWSLog_RejectsForeignOrigin pins the defence against cross-site WebSocket
// hijacking.
//
// This endpoint authenticates with a COOKIE, and browsers attach cookies to
// WebSocket handshakes from any origin while the same-origin policy does not
// apply to sockets. The shem endpoint's upgrader sets
// CheckOrigin: func(*http.Request) bool { return true }, which is fine there —
// it authenticates with a bearer API key a browser never holds — but reusing
// it here would let any page the operator visits open this socket and read the
// ticket's entire activity log, which carries repository contents and agent
// output.
func TestWSLog_RejectsForeignOrigin(t *testing.T) {
	srv, _, ticketID, cookie := setupWSLogTest(t)

	conn, resp, err := dial(t, srv, ticketID, cookie, "https://evil.example")
	if err == nil {
		conn.Close()
		t.Fatal("a cross-origin handshake succeeded: any site the operator visits could read this ticket's log")
	}
	if resp != nil {
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("status = %d, want 403", resp.StatusCode)
		}
	}
}

// TestWSLog_MissingOriginRejected: a browser always sends Origin on a
// WebSocket handshake, so an absent one cannot be trusted as same-origin.
func TestWSLog_MissingOriginRejected(t *testing.T) {
	srv, _, ticketID, cookie := setupWSLogTest(t)

	conn, resp, err := dial(t, srv, ticketID, cookie, "")
	if err == nil {
		conn.Close()
		t.Fatal("handshake with no Origin header succeeded")
	}
	if resp != nil {
		resp.Body.Close()
	}
}

// TestWSLog_UnsubscribesOnDisconnect: the SSE handler released its broker
// subscription via defer cancel() on r.Context().Done(). A socket that never
// unsubscribes leaks a channel per closed tab and, because Publish walks every
// subscriber, slows every later entry.
func TestWSLog_UnsubscribesOnDisconnect(t *testing.T) {
	srv, broker, ticketID, cookie := setupWSLogTest(t)

	conn, _, err := dial(t, srv, ticketID, cookie, srv.URL)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	waitForSubscribers(t, broker, ticketID, 1)

	if err := conn.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	waitForSubscribers(t, broker, ticketID, 0)
}

func waitForSubscribers(t *testing.T, broker *sse.Broker, ticketID string, want int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if broker.SubscriberCount(ticketID) == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("broker subscribers for %s = %d, want %d", ticketID, broker.SubscriberCount(ticketID), want)
}
