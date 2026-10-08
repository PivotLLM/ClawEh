package slack

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/slack-go/slack"

	"github.com/PivotLLM/ClawEh/bus"
	"github.com/PivotLLM/ClawEh/channels"
)

// Send says why Slack refused a message: a conversation the app cannot post
// to is ErrRecipientNotFound; any other API error stays ErrTemporary.
func TestSend_ClassifiesAPIErrors(t *testing.T) {
	tests := []struct {
		name string
		body string
		want error
	}{
		{"channel not found", `{"ok":false,"error":"channel_not_found"}`, channels.ErrRecipientNotFound},
		{"not in channel", `{"ok":false,"error":"not_in_channel"}`, channels.ErrRecipientNotFound},
		{"archived", `{"ok":false,"error":"is_archived"}`, channels.ErrRecipientNotFound},
		{"internal error", `{"ok":false,"error":"internal_error"}`, channels.ErrTemporary},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if _, err := io.WriteString(w, tt.body); err != nil {
					t.Errorf("write response: %v", err)
				}
			}))
			t.Cleanup(srv.Close)

			c := &SlackChannel{
				BaseChannel: channels.NewBaseChannel("slack", nil, nil, nil),
				api:         slack.New("xoxb-test", slack.OptionAPIURL(srv.URL+"/")),
			}
			c.SetRunning(true)
			err := c.Send(context.Background(), bus.OutboundMessage{Channel: "slack", ChatID: "C123", Content: "hello"})
			if !errors.Is(err, tt.want) {
				t.Fatalf("Send = %v, want %v", err, tt.want)
			}
			// Slack's reason stays in the text that is logged and alerted.
			var apiErr slack.SlackErrorResponse
			if !errors.As(err, &apiErr) || !strings.Contains(err.Error(), apiErr.Err) {
				t.Errorf("Send = %q, want it to keep Slack's error", err)
			}
		})
	}
}
