package slack

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/slack-go/slack"
	"github.com/slack-go/slack/slackevents"

	"github.com/PivotLLM/ClawEh/bus"
	"github.com/PivotLLM/ClawEh/channels"
	"github.com/PivotLLM/ClawEh/config"
	"github.com/PivotLLM/ClawEh/internal/clock"
)

// newNamingChannel is a running Slack channel whose fake API answers
// users.info with usersInfo and counts the calls.
func newNamingChannel(t *testing.T, usersInfo string) (*SlackChannel, *bus.MessageBus, *atomic.Int32, *clock.Fake) {
	t.Helper()
	var lookups atomic.Int32
	fake := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/users.info") {
			http.NotFound(w, r)
			return
		}
		lookups.Add(1)
		if _, err := io.WriteString(w, usersInfo); err != nil {
			t.Errorf("write response: %v", err)
		}
	}))
	t.Cleanup(srv.Close)

	mb := bus.NewMessageBus()
	t.Cleanup(mb.Close)
	c := &SlackChannel{
		BaseChannel: channels.NewBaseChannel("slack", config.SlackConfig{}, mb, []string{"*"}),
		api:         slack.New("xoxb-test", slack.OptionAPIURL(srv.URL+"/")),
		botUserID:   "UBOT",
		clock:       fake,
	}
	c.ctx, c.cancel = context.WithCancel(context.Background())
	t.Cleanup(c.cancel)
	c.SetRunning(true)
	return c, mb, &lookups, fake
}

// TestHandleMessageEvent_NamesSenderOnce: the sender is named from users.info
// on the first message and from the cache after. A failed lookup leaves the
// message unnamed but delivered, and is retried once userLookupRetry has
// passed; a found name is never looked up again.
func TestHandleMessageEvent_NamesSenderOnce(t *testing.T) {
	tests := []struct {
		name        string
		usersInfo   string
		wantDisplay string
		wantUser    string
		wantLookups int32 // after a message past the retry window
	}{
		{
			name:        "profile display name",
			usersInfo:   `{"ok":true,"user":{"id":"U1","name":"alice","real_name":"Alice Smith","profile":{"display_name":"Ali"}}}`,
			wantDisplay: "Ali",
			wantUser:    "alice",
			wantLookups: 1,
		},
		{
			name:        "real name when no display name",
			usersInfo:   `{"ok":true,"user":{"id":"U1","name":"alice","real_name":"Alice Smith","profile":{"display_name":""}}}`,
			wantDisplay: "Alice Smith",
			wantUser:    "alice",
			wantLookups: 1,
		},
		{
			name:        "failed lookup leaves the sender unnamed and is retried",
			usersInfo:   `{"ok":false,"error":"user_not_found"}`,
			wantLookups: 2,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, mb, lookups, fake := newNamingChannel(t, tt.usersInfo)

			send := func(ts string) bus.InboundMessage {
				c.handleMessageEvent(&slackevents.MessageEvent{User: "U1", Channel: "D1", Text: "hello", TimeStamp: ts})
				return nextInbound(t, mb)
			}
			check := func(ts string, msg bus.InboundMessage) {
				t.Helper()
				if msg.Sender.DisplayName != tt.wantDisplay || msg.Sender.Username != tt.wantUser {
					t.Fatalf("message %s: sender = (%q, %q), want (%q, %q)",
						ts, msg.Sender.DisplayName, msg.Sender.Username, tt.wantDisplay, tt.wantUser)
				}
				if msg.Sender.PlatformID != "U1" {
					t.Fatalf("message %s: PlatformID = %q, want U1", ts, msg.Sender.PlatformID)
				}
			}

			// Within the retry window: one lookup, whatever its outcome.
			for _, ts := range []string{"1.0001", "1.0002"} {
				check(ts, send(ts))
			}
			fake.Advance(userLookupRetry - time.Second)
			check("1.0003", send("1.0003"))
			if got := lookups.Load(); got != 1 {
				t.Fatalf("users.info called %d times within the retry window, want 1", got)
			}

			fake.Advance(time.Second)
			check("1.0004", send("1.0004"))
			if got := lookups.Load(); got != tt.wantLookups {
				t.Fatalf("users.info called %d times after the retry window, want %d", got, tt.wantLookups)
			}
		})
	}
}
