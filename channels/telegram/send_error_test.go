package telegram

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/PivotLLM/ClawEh/bus"
	"github.com/PivotLLM/ClawEh/channels"
	"github.com/PivotLLM/ClawEh/config"
)

// Send says why Telegram refused a message: a chat the bot cannot reach is
// ErrRecipientNotFound (not retried, and not tried again as plain text);
// any other API error stays ErrTemporary.
func TestSend_ClassifiesAPIErrors(t *testing.T) {
	tests := []struct {
		name      string
		body      string
		want      error
		wantSends int32
	}{
		{"chat not found", `{"ok":false,"error_code":400,"description":"Bad Request: chat not found"}`, channels.ErrRecipientNotFound, 1},
		{"blocked by the user", `{"ok":false,"error_code":403,"description":"Forbidden: bot was blocked by the user"}`, channels.ErrRecipientNotFound, 1},
		{"user deactivated", `{"ok":false,"error_code":403,"description":"Forbidden: user is deactivated"}`, channels.ErrRecipientNotFound, 1},
		{"kicked from the group", `{"ok":false,"error_code":403,"description":"Forbidden: bot was kicked from the group chat"}`, channels.ErrRecipientNotFound, 1},
		{"server error", `{"ok":false,"error_code":500,"description":"Internal Server Error"}`, channels.ErrTemporary, 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var sends atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				body := `{"ok":true,"result":true}`
				switch {
				case strings.HasSuffix(r.URL.Path, "/getMe"):
					body = `{"ok":true,"result":{"id":1,"is_bot":true,"first_name":"t","username":"t_bot"}}`
				case strings.HasSuffix(r.URL.Path, "/sendMessage"):
					sends.Add(1)
					body = tt.body
				}
				if _, err := w.Write([]byte(body)); err != nil {
					t.Errorf("write %s: %v", r.URL.Path, err)
				}
			}))
			t.Cleanup(srv.Close)

			ch, err := NewTelegramChannelFromConfig(config.TelegramBotConfig{Token: testToken, BaseURL: srv.URL}, bus.NewMessageBus())
			require.NoError(t, err)
			ch.SetRunning(true)
			t.Cleanup(func() { ch.SetRunning(false) })

			err = ch.Send(context.Background(), bus.OutboundMessage{Channel: "telegram", ChatID: "12345", Content: "hello"})
			if !errors.Is(err, tt.want) {
				t.Fatalf("Send = %v, want %v", err, tt.want)
			}
			if got := sends.Load(); got != tt.wantSends {
				t.Fatalf("sendMessage called %d times, want %d", got, tt.wantSends)
			}
			require.NotContains(t, err.Error(), strings.Split(testToken, ":")[1])
		})
	}
}
