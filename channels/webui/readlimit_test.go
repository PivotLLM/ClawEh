package webui

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/PivotLLM/ClawEh/utils"
)

// TestReadLimit: an authenticated WebUI socket still answers a normal message,
// and a frame over maxMessageBytes closes it.
func TestReadLimit(t *testing.T) {
	const token = "webui-token-abcdefghijkl"
	c := testChannel(t, token)
	if err := c.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := c.Stop(context.Background()); err != nil {
			t.Errorf("Stop: %v", err)
		}
	})

	hs := httptest.NewServer(c)
	t.Cleanup(hs.Close)
	wsURL := "ws" + strings.TrimPrefix(hs.URL, "http") + "/webui/ws"
	hdr := http.Header{"Authorization": {"Bearer " + token}}

	conn, resp, err := websocket.DefaultDialer.Dial(wsURL, hdr)
	if resp != nil {
		if closeErr := resp.Body.Close(); closeErr != nil {
			t.Errorf("close dial response body: %v", closeErr)
		}
	}
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer utils.CloseQuietly(conn)

	if err := conn.WriteJSON(WebUIMessage{Type: TypePing, ID: "p1"}); err != nil {
		t.Fatal(err)
	}
	var pong WebUIMessage
	if err := conn.ReadJSON(&pong); err != nil || pong.Type != TypePong || pong.ID != "p1" {
		t.Fatalf("ping not answered: err=%v msg=%+v", err, pong)
	}

	big := `{"type":"ping","id":"` + strings.Repeat("x", maxMessageBytes) + `"}`
	if err := conn.WriteMessage(websocket.TextMessage, []byte(big)); err != nil {
		t.Logf("oversized write ended early (server closed): %v", err)
	}
	if err := conn.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatalf("set read deadline: %v", err)
	}
	if _, _, err := conn.ReadMessage(); err == nil {
		t.Fatal("frame over maxMessageBytes was accepted")
	}
}
