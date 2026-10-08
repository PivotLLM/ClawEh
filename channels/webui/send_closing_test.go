package webui

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gorilla/websocket"

	"github.com/PivotLLM/ClawEh/channels"
)

// serverConn returns the server side of a live WebSocket connection whose
// client end stays open for the test.
func serverConn(t *testing.T) *websocket.Conn {
	t.Helper()
	got := make(chan *websocket.Conn, 1)
	upgrader := websocket.Upgrader{}
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("upgrade: %v", err)
			return
		}
		got <- conn
	}))
	t.Cleanup(hs.Close)
	client, resp, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(hs.URL, "http"), nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
	t.Cleanup(func() { _ = client.Close() })
	conn := <-got
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

func addConn(c *WebUIChannel, id, sessionID string, conn *websocket.Conn) *webuiConn {
	pc := &webuiConn{id: id, conn: conn, sessionID: sessionID}
	c.connections.Store(id, pc)
	return pc
}

// A tab that closed (or is closing) while a reply is sent makes the session
// offline, never a send failure that raises the channel alert.
func TestBroadcast_ClosingConnectionIsOffline(t *testing.T) {
	t.Run("marked closed", func(t *testing.T) {
		c := testChannel(t, "tok")
		pc := addConn(c, "c1", "s1", serverConn(t))
		pc.close()
		err := c.broadcastToSession("webui:s1", newMessage(TypeMessageCreate, map[string]any{"content": "hi"}))
		if !errors.Is(err, channels.ErrRecipientOffline) {
			t.Fatalf("err = %v, want ErrRecipientOffline", err)
		}
	})
	t.Run("socket closed underneath", func(t *testing.T) {
		c := testChannel(t, "tok")
		conn := serverConn(t)
		addConn(c, "c1", "s1", conn)
		_ = conn.NetConn().Close()
		err := c.broadcastToSession("webui:s1", newMessage(TypeMessageCreate, map[string]any{"content": "hi"}))
		if !errors.Is(err, channels.ErrRecipientOffline) {
			t.Fatalf("err = %v, want ErrRecipientOffline", err)
		}
	})
	t.Run("one open tab of two", func(t *testing.T) {
		c := testChannel(t, "tok")
		addConn(c, "c1", "s1", serverConn(t)).close()
		addConn(c, "c2", "s1", serverConn(t))
		if err := c.broadcastToSession("webui:s1", newMessage(TypeMessageCreate, map[string]any{"content": "hi"})); err != nil {
			t.Fatalf("err = %v, want nil", err)
		}
	})
}

// A write that fails on an open connection for a reason of our own is a real
// send failure and says why.
func TestBroadcast_FaultIsSendFailed(t *testing.T) {
	c := testChannel(t, "tok")
	addConn(c, "c1", "s1", serverConn(t))
	err := c.broadcastToSession("webui:s1", newMessage(TypeMessageCreate, map[string]any{"bad": func() {}}))
	if !errors.Is(err, channels.ErrSendFailed) {
		t.Fatalf("err = %v, want ErrSendFailed", err)
	}
	if !strings.Contains(err.Error(), "unsupported type") {
		t.Errorf("err = %q, want the encoder's reason", err)
	}
}
