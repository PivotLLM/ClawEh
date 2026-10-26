package webui

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/PivotLLM/ClawEh/bus"
	"github.com/PivotLLM/ClawEh/config"
)

// TestHandleMessageSend_NamesLoginAccount: a message sent on a socket opened
// with a login session carries the session's username as the sender's display
// name only, so the label reads "[From: alice]", while the sender id stays the
// fixed webui-user that allow-from rules and session keys use.
func TestHandleMessageSend_NamesLoginAccount(t *testing.T) {
	mb := bus.NewMessageBus()
	t.Cleanup(mb.Close)
	c, err := NewWebUIChannel(config.WebUIConfig{Token: "webui-token-abcdefghijkl", AllowFrom: []string{"*"}}, mb)
	if err != nil {
		t.Fatal(err)
	}
	if startErr := c.Start(t.Context()); startErr != nil {
		t.Fatal(startErr)
	}
	t.Cleanup(func() {
		if stopErr := c.Stop(context.Background()); stopErr != nil {
			t.Errorf("Stop: %v", stopErr)
		}
		SetSessionValidator(nil)
	})
	SetSessionValidator(func(r *http.Request) (string, bool) {
		ck, cookieErr := r.Cookie("claw_session")
		return "alice", cookieErr == nil && ck.Value == "live"
	})

	srv := httptest.NewServer(c)
	t.Cleanup(srv.Close)
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/webui/ws?session_id=s1"
	conn, resp, err := websocket.DefaultDialer.Dial(wsURL, http.Header{"Cookie": {"claw_session=live"}})
	if resp != nil {
		if closeErr := resp.Body.Close(); closeErr != nil {
			t.Errorf("close dial response body: %v", closeErr)
		}
	}
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() {
		if closeErr := conn.Close(); closeErr != nil {
			t.Errorf("close websocket: %v", closeErr)
		}
	})

	send := WebUIMessage{Type: TypeMessageSend, ID: "m1", Payload: map[string]any{"content": "hello"}}
	if err := conn.WriteJSON(send); err != nil {
		t.Fatalf("write: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	msg, ok := mb.ConsumeInbound(ctx)
	if !ok {
		t.Fatal("no inbound message published")
	}
	if msg.Sender.DisplayName != "alice" || msg.Sender.Username != "" {
		t.Fatalf("sender = (display %q, username %q), want (display alice, no username)", msg.Sender.DisplayName, msg.Sender.Username)
	}
	if msg.Sender.PlatformID != "webui-user" {
		t.Fatalf("PlatformID = %q, want webui-user", msg.Sender.PlatformID)
	}
}
