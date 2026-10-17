package channels_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/PivotLLM/ClawEh/bus"
	"github.com/PivotLLM/ClawEh/channels"
	"github.com/PivotLLM/ClawEh/channels/telegram"
	"github.com/PivotLLM/ClawEh/config"
	"github.com/PivotLLM/ClawEh/logger"
)

const (
	deliveryToken  = "1234567890:secretSECRETsecretSECRETsecretSECRE"
	deliveryGetMe  = `{"ok":true,"result":{"id":1,"is_bot":true,"first_name":"t","username":"t_bot"}}`
	deliverySentOK = `{"ok":true,"result":{"message_id":1,"date":0,"chat":{"id":12345,"type":"private"}}}`
)

// fakeBotAPI is a Telegram Bot API whose sendMessage answers with replies in
// turn, repeating the last one, and counts the calls.
type fakeBotAPI struct {
	srv   *httptest.Server
	sends atomic.Int32
}

func newFakeBotAPI(t *testing.T, replies ...string) *fakeBotAPI {
	t.Helper()
	f := &fakeBotAPI{}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		body := `{"ok":true,"result":true}`
		switch {
		case strings.HasSuffix(r.URL.Path, "/getMe"):
			body = deliveryGetMe
		case strings.HasSuffix(r.URL.Path, "/sendMessage"):
			n := int(f.sends.Add(1))
			body = replies[min(n, len(replies))-1]
		}
		if _, err := io.WriteString(w, body); err != nil {
			t.Errorf("write %s: %v", r.URL.Path, err)
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

// newTelegramManager returns a manager sending through a running Telegram
// channel "telegram-alice" whose Bot API is api, and the alerts it raises.
func newTelegramManager(t *testing.T, api *fakeBotAPI) (*channels.Manager, *recorder) {
	t.Helper()
	ch, err := telegram.NewTelegramChannelFromConfig(config.TelegramBotConfig{
		ID: "alice", Token: deliveryToken, BaseURL: api.srv.URL,
	}, bus.NewMessageBus())
	if err != nil {
		t.Fatal(err)
	}
	ch.SetRunning(true)
	t.Cleanup(func() { ch.SetRunning(false) })
	m := channels.NewTestManagerWith("telegram-alice", ch)
	rec := &recorder{}
	m.SetAlerter(rec)
	return m, rec
}

// A message sent through the manager to Telegram ends as Telegram's answer
// says: a chat the bot cannot reach is reported as not found, logged at WARN,
// never retried and never alerted; a rate limit, a server error or a dropped
// connection is retried; any other refusal fails at once and alerts, as does
// a retryable failure that outlasts the retries. OnDelivery gets the same
// reason, Telegram's own words kept and the token left out.
func TestTelegramDelivery_ThroughManager(t *testing.T) {
	const (
		notFound   = `{"ok":false,"error_code":400,"description":"Bad Request: chat not found"}`
		blocked    = `{"ok":false,"error_code":403,"description":"Forbidden: bot was blocked by the user"}`
		deact      = `{"ok":false,"error_code":403,"description":"Forbidden: user is deactivated"}`
		initiate   = `{"ok":false,"error_code":403,"description":"Forbidden: bot can't initiate conversation with a user"}`
		kicked     = `{"ok":false,"error_code":403,"description":"Forbidden: bot was kicked from the group chat"}`
		toBots     = `{"ok":false,"error_code":403,"description":"Forbidden: bot can't send messages to bots"}`
		upgraded   = `{"ok":false,"error_code":400,"description":"Bad Request: group chat was upgraded to a supergroup chat","parameters":{"migrate_to_chat_id":-1001234}}`
		rateLimit  = `{"ok":false,"error_code":429,"description":"Too Many Requests: retry after 5","parameters":{"retry_after":5}}`
		badRequest = `{"ok":false,"error_code":400,"description":"Bad Request: message is too long"}`
		parseErr   = `{"ok":false,"error_code":400,"description":"Bad Request: can't parse entities: unexpected end tag"}`
		serverErr  = `{"ok":false,"error_code":500,"description":"Internal Server Error"}`
	)
	allAttempts := int32(channels.MaxSendRetries + 1)
	tests := []struct {
		name      string
		replies   []string
		want      error // nil: delivered
		wantText  string
		wantSends int32
		wantAlert bool
		wantWarn  bool
	}{
		{"chat not found", []string{notFound}, channels.ErrRecipientNotFound, "chat not found", 1, false, true},
		{"blocked by the user", []string{blocked}, channels.ErrRecipientNotFound, "blocked by the user", 1, false, true},
		{"user deactivated", []string{deact}, channels.ErrRecipientNotFound, "user is deactivated", 1, false, true},
		{"never started a conversation", []string{initiate}, channels.ErrRecipientNotFound, "initiate conversation", 1, false, true},
		{"kicked from the group", []string{kicked}, channels.ErrRecipientNotFound, "kicked", 1, false, true},
		{"another bot", []string{toBots}, channels.ErrRecipientNotFound, "messages to bots", 1, false, true},
		{"upgraded to a supergroup", []string{upgraded}, channels.ErrRecipientNotFound, "upgraded to a supergroup", 1, false, true},
		{"other bad request", []string{badRequest}, channels.ErrSendFailed, "message is too long", 1, true, false},
		{"rate limited throughout", []string{rateLimit}, channels.ErrRateLimit, "retry after 5", allAttempts, true, false},
		{"server error throughout", []string{serverErr}, channels.ErrTemporary, "Internal Server Error", allAttempts, true, false},
		{"rate limited once", []string{rateLimit, deliverySentOK}, nil, "", 2, false, false},
		{"server error once", []string{serverErr, deliverySentOK}, nil, "", 2, false, false},
		{"html refused, plain text sent", []string{parseErr, deliverySentOK}, nil, "", 2, false, false},
		{"html refused, chat not found", []string{parseErr, notFound}, channels.ErrRecipientNotFound, "chat not found", 2, false, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var logs syncBuffer
			restore := logger.RedirectForTest(&logs)
			defer restore()

			api := newFakeBotAPI(t, tt.replies...)
			m, rec := newTelegramManager(t, api)

			delivered := make(chan error, 1)
			err := m.SendMessage(context.Background(), bus.OutboundMessage{
				Channel: "telegram-alice", ChatID: "12345", Content: "hello",
				OnDelivery: func(err error) { delivered <- err },
			})
			checkDeliveryErr(t, "SendMessage", err, tt.want, tt.wantText)
			select {
			case got := <-delivered:
				checkDeliveryErr(t, "OnDelivery", got, tt.want, tt.wantText)
			case <-time.After(5 * time.Second):
				t.Fatal("OnDelivery was not called")
			}

			if got := api.sends.Load(); got != tt.wantSends {
				t.Fatalf("sendMessage called %d times, want %d", got, tt.wantSends)
			}
			// Each retry waited once; the plain-text resend is not a retry.
			wantWaits := int(tt.wantSends) - 1
			if tt.replies[0] == parseErr {
				wantWaits = 0
			}
			waits := channels.ManagerWaits(m)
			if len(waits) != wantWaits {
				t.Fatalf("retry waits = %v, want %d", waits, wantWaits)
			}
			// A rate limit waits Telegram's retry_after (5 s) plus the padding,
			// every time.
			if tt.replies[0] == rateLimit {
				for i, w := range waits {
					if want := 5*time.Second + channels.RetryAfterPadding; w != want {
						t.Fatalf("retry wait %d = %v, want %v", i, w, want)
					}
				}
			}

			alerts := rec.snapshot()
			if tt.wantAlert {
				if len(alerts) != 1 || alerts[0].Title != "Channel send failed" {
					t.Fatalf("alerts = %+v, want one Channel send failed", alerts)
				}
				if !strings.Contains(alerts[0].Details, tt.wantText) {
					t.Fatalf("alert details %q do not say why (%q)", alerts[0].Details, tt.wantText)
				}
			} else if len(alerts) != 0 {
				t.Fatalf("alerts = %+v, want none", alerts)
			}

			out := logs.String()
			warned := strings.Contains(out, `"level":"warn"`) && strings.Contains(out, "Send dropped: recipient not found")
			if warned != tt.wantWarn {
				t.Fatalf("WARN Send dropped logged = %v, want %v; logs:\n%s", warned, tt.wantWarn, out)
			}
			if tt.wantWarn && strings.Contains(out, `"message":"Send failed"`) {
				t.Fatalf("a recipient not found was logged as a failed send:\n%s", out)
			}
			if strings.Contains(out, strings.Split(deliveryToken, ":")[1]) {
				t.Fatal("logs carry the bot token")
			}
		})
	}
}

func checkDeliveryErr(t *testing.T, what string, err, want error, wantText string) {
	t.Helper()
	if want == nil {
		if err != nil {
			t.Fatalf("%s = %v, want delivered", what, err)
		}
		return
	}
	if !errors.Is(err, want) {
		t.Fatalf("%s = %v, want %v", what, err, want)
	}
	if !strings.Contains(err.Error(), wantText) {
		t.Fatalf("%s = %q, want Telegram's reason %q", what, err, wantText)
	}
	if strings.Contains(err.Error(), strings.Split(deliveryToken, ":")[1]) {
		t.Fatalf("%s carries the bot token: %q", what, err)
	}
}

// A message too long for one Telegram message stops at the first part the
// chat cannot take, whether the manager split it (over 4,000 characters) or
// the channel did (its HTML outgrew Telegram's limit); a part that failed
// otherwise leaves the rest to be tried.
func TestTelegramDelivery_SplitStopsAtUnreachableChat(t *testing.T) {
	const (
		unreachable = `{"ok":false,"error_code":403,"description":"Forbidden: bot was blocked by the user"}`
		badRequest  = `{"ok":false,"error_code":400,"description":"Bad Request: message is too long"}`
	)
	managerSplit := strings.Repeat("word ", 2000)          // 10,000 characters: three parts
	channelSplit := strings.Repeat("a < b & c > d\n", 280) // under 4,000, over 4,096 as HTML
	if n := len([]rune(channelSplit)); n > 4000 {
		t.Fatalf("the channel-split message must fit the manager's limit, has %d characters", n)
	}
	tests := []struct {
		name      string
		content   string
		reply     string
		want      error
		wantSends func(int32) bool
	}{
		{"manager split, chat unreachable", managerSplit, unreachable, channels.ErrRecipientNotFound, func(n int32) bool { return n == 1 }},
		{"channel split, delivered", channelSplit, deliverySentOK, nil, func(n int32) bool { return n >= 2 }},
		{"channel split, chat unreachable", channelSplit, unreachable, channels.ErrRecipientNotFound, func(n int32) bool { return n == 1 }},
		{"manager split, send failed", managerSplit, badRequest, channels.ErrSendFailed, func(n int32) bool { return n == 3 }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			api := newFakeBotAPI(t, tt.reply)
			m, _ := newTelegramManager(t, api)
			err := m.SendMessage(context.Background(), bus.OutboundMessage{
				Channel: "telegram-alice", ChatID: "12345", Content: tt.content,
			})
			if !errors.Is(err, tt.want) || (tt.want == nil && err != nil) {
				t.Fatalf("SendMessage = %v, want %v", err, tt.want)
			}
			if n := api.sends.Load(); !tt.wantSends(n) {
				t.Fatalf("sendMessage called %d times", n)
			}
		})
	}
}
