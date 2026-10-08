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
// ErrRecipientNotFound, a 429 ErrRateLimit, another 4xx ErrSendFailed and
// anything else ErrTemporary. Only an HTML parse error is tried again as
// plain text. The error keeps Telegram's reason.
func TestSend_ClassifiesAPIErrors(t *testing.T) {
	tests := []struct {
		name      string
		body      string
		plainBody string // reply to a second sendMessage, when one is expected
		want      error
		wantSends int32
		wantText  string
	}{
		{"chat not found", `{"ok":false,"error_code":400,"description":"Bad Request: chat not found"}`, "", channels.ErrRecipientNotFound, 1, "chat not found"},
		{"blocked by the user", `{"ok":false,"error_code":403,"description":"Forbidden: bot was blocked by the user"}`, "", channels.ErrRecipientNotFound, 1, ""},
		{"user deactivated", `{"ok":false,"error_code":403,"description":"Forbidden: user is deactivated"}`, "", channels.ErrRecipientNotFound, 1, ""},
		{"kicked from the group", `{"ok":false,"error_code":403,"description":"Forbidden: bot was kicked from the group chat"}`, "", channels.ErrRecipientNotFound, 1, ""},
		{"never started a conversation", `{"ok":false,"error_code":403,"description":"Forbidden: bot can't initiate conversation with a user"}`, "", channels.ErrRecipientNotFound, 1, ""},
		{"another bot", `{"ok":false,"error_code":403,"description":"Forbidden: bot can't send messages to bots"}`, "", channels.ErrRecipientNotFound, 1, ""},
		{"upgraded to a supergroup", `{"ok":false,"error_code":400,"description":"Bad Request: group chat was upgraded to a supergroup chat","parameters":{"migrate_to_chat_id":-1001234}}`, "", channels.ErrRecipientNotFound, 1, ""},
		{"server error", `{"ok":false,"error_code":500,"description":"Internal Server Error"}`, "", channels.ErrTemporary, 1, "Internal Server Error"},
		{"any other 403", `{"ok":false,"error_code":403,"description":"Forbidden: not enough rights to send text messages to the chat"}`, "", channels.ErrRecipientNotFound, 1, "not enough rights"},
		{"rate limited", `{"ok":false,"error_code":429,"description":"Too Many Requests: retry after 5","parameters":{"retry_after":5}}`, "", channels.ErrRateLimit, 1, "Too Many Requests"},
		{"other bad request", `{"ok":false,"error_code":400,"description":"Bad Request: message is too long"}`, "", channels.ErrSendFailed, 1, "message is too long"},
		{"html parse error, plain text sent", `{"ok":false,"error_code":400,"description":"Bad Request: can't parse entities: unexpected end tag"}`, `{"ok":true,"result":{"message_id":1,"date":0,"chat":{"id":12345,"type":"private"}}}`, nil, 2, ""},
		{"html parse error, plain text fails", `{"ok":false,"error_code":400,"description":"Bad Request: can't parse entities: unexpected end tag"}`, `{"ok":false,"error_code":500,"description":"Internal Server Error"}`, channels.ErrTemporary, 2, "Internal Server Error"},
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
					body = tt.body
					if sends.Add(1) > 1 {
						body = tt.plainBody
					}
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
			if tt.want == nil {
				require.NoError(t, err)
			} else if !errors.Is(err, tt.want) {
				t.Fatalf("Send = %v, want %v", err, tt.want)
			}
			if got := sends.Load(); got != tt.wantSends {
				t.Fatalf("sendMessage called %d times, want %d", got, tt.wantSends)
			}
			if err != nil {
				require.Contains(t, err.Error(), tt.wantText)
				require.NotContains(t, err.Error(), strings.Split(testToken, ":")[1])
			}
		})
	}
}
