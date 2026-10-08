package telegram

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/PivotLLM/ClawEh/bus"
	"github.com/PivotLLM/ClawEh/channels"
	"github.com/PivotLLM/ClawEh/config"
)

// sentMessage is what one sendMessage request asked for.
type sentMessage struct {
	Text            string          `json:"text"`
	ReplyParameters json.RawMessage `json:"reply_parameters"`
}

// sendServer answers getMe, and each sendMessage with replies[n] for the
// n-th call (ok once the list runs out); it records every sendMessage.
func sendServer(t *testing.T, replies []string) (*TelegramChannel, func() []sentMessage) {
	t.Helper()
	var mu sync.Mutex
	var sent []sentMessage
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		body := `{"ok":true,"result":true}`
		switch {
		case strings.HasSuffix(r.URL.Path, "/getMe"):
			body = `{"ok":true,"result":{"id":1,"is_bot":true,"first_name":"t","username":"t_bot"}}`
		case strings.HasSuffix(r.URL.Path, "/sendMessage"):
			raw, err := io.ReadAll(r.Body)
			if err != nil {
				t.Errorf("read body: %v", err)
			}
			var m sentMessage
			if err := json.Unmarshal(raw, &m); err != nil {
				t.Errorf("decode sendMessage %q: %v", raw, err)
			}
			mu.Lock()
			n := len(sent)
			sent = append(sent, m)
			mu.Unlock()
			body = `{"ok":true,"result":{"message_id":1,"date":0,"chat":{"id":12345,"type":"private"}}}`
			if n < len(replies) && replies[n] != "" {
				body = replies[n]
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
	return ch, func() []sentMessage {
		mu.Lock()
		defer mu.Unlock()
		return append([]sentMessage(nil), sent...)
	}
}

const serverError = `{"ok":false,"error_code":500,"description":"Internal Server Error"}`

// A message whose HTML is too long for one Telegram message goes out in
// parts; a retry after a failed part sends only the parts still missing, and
// only the first part is a reply.
func TestSend_RetryResumesAfterDeliveredParts(t *testing.T) {
	ch, sent := sendServer(t, []string{"", serverError})
	// Every "&" becomes "&amp;" in HTML: 1,000 characters expand past 4,096.
	msg := bus.OutboundMessage{Channel: "telegram", ChatID: "12345", Content: strings.Repeat("&", 1000), ReplyToMessageID: "42"}
	ctx := channels.WithSendProgress(context.Background())

	err := ch.Send(ctx, msg)
	if !errors.Is(err, channels.ErrTemporary) {
		t.Fatalf("first attempt = %v, want ErrTemporary", err)
	}
	require.NoError(t, ch.Send(ctx, msg))

	got := sent()
	require.Len(t, got, 3, "part 1, part 2 (failed), part 2 again")
	require.NotEqual(t, got[0].Text, got[1].Text)
	require.Equal(t, got[1].Text, got[2].Text, "the retry resends the failed part")
	require.Equal(t, strings.Repeat("&amp;", 1000), got[0].Text+got[2].Text, "the parts make up the message once")
	require.NotEmpty(t, got[0].ReplyParameters, "the first part is the reply")
	require.Empty(t, got[1].ReplyParameters)
	require.Empty(t, got[2].ReplyParameters)
}

// A new message (no progress carried over) is sent whole.
func TestSend_NewMessageSendsEveryPart(t *testing.T) {
	ch, sent := sendServer(t, nil)
	msg := bus.OutboundMessage{Channel: "telegram", ChatID: "12345", Content: strings.Repeat("&", 1000)}
	require.NoError(t, ch.Send(channels.WithSendProgress(context.Background()), msg))
	require.NoError(t, ch.Send(channels.WithSendProgress(context.Background()), msg))
	require.Len(t, sent(), 4)
}

// A reply to a message that no longer exists is sent once more without the
// reply link instead of failing.
func TestSend_ReplyTargetGone(t *testing.T) {
	gone := `{"ok":false,"error_code":400,"description":"Bad Request: message to be replied not found"}`
	tests := []struct {
		name      string
		replyTo   string
		replies   []string
		want      error
		wantSends int
	}{
		{"resent without the reply link", "42", []string{gone}, nil, 2},
		{"resend fails", "42", []string{gone, serverError}, channels.ErrTemporary, 2},
		{"not a reply: a plain 400", "", []string{gone}, channels.ErrSendFailed, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ch, sent := sendServer(t, tt.replies)
			err := ch.Send(context.Background(), bus.OutboundMessage{Channel: "telegram", ChatID: "12345", Content: "hello", ReplyToMessageID: tt.replyTo})
			if tt.want == nil {
				require.NoError(t, err)
			} else if !errors.Is(err, tt.want) {
				t.Fatalf("Send = %v, want %v", err, tt.want)
			}
			got := sent()
			require.Len(t, got, tt.wantSends)
			if tt.wantSends == 2 {
				require.NotEmpty(t, got[0].ReplyParameters)
				require.Empty(t, got[1].ReplyParameters)
				require.Equal(t, got[0].Text, got[1].Text)
			}
		})
	}
}
