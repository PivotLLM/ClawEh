package telegram

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/PivotLLM/ClawEh/bus"
	"github.com/PivotLLM/ClawEh/channels"
	"github.com/PivotLLM/ClawEh/config"
)

// apiReply is how the test API server answers one method: a status and a
// body, or no answer until the request is abandoned (hang).
type apiReply struct {
	status int
	body   string
	hang   bool
}

// newCheckServer returns a bot whose API server answers getMe and
// getWebhookInfo as given.
func newCheckServer(t *testing.T, getMe, webhook apiReply) *TelegramChannel {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reply := webhook
		if strings.HasSuffix(r.URL.Path, "/getMe") {
			reply = getMe
		}
		if reply.hang {
			_, _ = io.Copy(io.Discard, r.Body) // lets the server notice the client hang up
			<-r.Context().Done()
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(reply.status)
		_, _ = w.Write([]byte(reply.body))
	}))
	t.Cleanup(srv.Close)
	ch, err := NewTelegramChannelFromConfig(config.TelegramBotConfig{Token: testToken, BaseURL: srv.URL}, bus.NewMessageBus())
	require.NoError(t, err)
	return ch
}

var (
	meOK      = apiReply{status: 200, body: `{"ok":true,"result":{"id":1,"is_bot":true,"first_name":"Alice"}}`}
	webhook3  = apiReply{status: 200, body: `{"ok":true,"result":{"url":"","has_custom_certificate":false,"pending_update_count":3}}`}
	badGW     = apiReply{status: 502, body: `<html>Bad Gateway</html>`}
	noAnswer  = apiReply{hang: true}
	tokenGone = apiReply{status: 401, body: `{"ok":false,"error_code":401,"description":"Unauthorized"}`}
)

func TestConnCheck(t *testing.T) {
	tests := []struct {
		name    string
		getMe   apiReply
		webhook apiReply
		want    channels.ConnCheckResult
	}{
		{"working with waiting messages", meOK, webhook3, channels.ConnCheckResult{Working: true, Waiting: 3, WaitingKnown: true}},
		{"getMe 502", badGW, webhook3, channels.ConnCheckResult{Reason: "502", Waiting: 3, WaitingKnown: true}},
		{"getMe refused", tokenGone, tokenGone, channels.ConnCheckResult{Reason: "401"}},
		{"getWebhookInfo failing", meOK, badGW, channels.ConnCheckResult{Working: true}},
		{"no answer", noAnswer, noAnswer, channels.ConnCheckResult{Reason: channels.ConnCheckNoResponse}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ch := newCheckServer(t, tt.getMe, tt.webhook)
			ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
			defer cancel()
			require.Equal(t, tt.want, ch.ConnCheck(ctx))
		})
	}
}

// A request that never reaches Telegram fails with "no response", and the
// reason never carries the token.
func TestConnCheck_Unreachable(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close()
	ch, err := NewTelegramChannelFromConfig(config.TelegramBotConfig{Token: testToken, BaseURL: url}, bus.NewMessageBus())
	require.NoError(t, err)

	r := ch.ConnCheck(context.Background())
	require.Equal(t, channels.ConnCheckResult{Reason: channels.ConnCheckNoResponse}, r)
}
