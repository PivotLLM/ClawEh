package slack

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/slack-go/slack"
	"github.com/slack-go/slack/slackevents"
	"github.com/slack-go/slack/socketmode"

	"github.com/PivotLLM/ClawEh/bus"
	"github.com/PivotLLM/ClawEh/channels"
	"github.com/PivotLLM/ClawEh/config"
	"github.com/PivotLLM/ClawEh/internal/clock"
)

// newCommandChannel is a running Slack channel on a fake Slack API that
// answers chat.postMessage with postReply, publishing inbound messages to the
// returned bus.
func newCommandChannel(t *testing.T, postReply string) (*SlackChannel, *bus.MessageBus) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if _, err := io.WriteString(w, postReply); err != nil {
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
	}
	c.userNameCache.Store("U1", "Alice")
	c.ctx, c.cancel = context.WithCancel(context.Background())
	t.Cleanup(c.cancel)
	c.SetRunning(true)
	return c, mb
}

func nextInbound(t *testing.T, mb *bus.MessageBus) bus.InboundMessage {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	msg, ok := mb.ConsumeInbound(ctx)
	if !ok {
		t.Fatal("no inbound message published")
	}
	return msg
}

func slashEvent(command, text, channelID string) socketmode.Event {
	return socketmode.Event{
		Type: socketmode.EventTypeSlashCommand,
		Data: slack.SlashCommand{
			Command:     command,
			Text:        text,
			ChannelID:   channelID,
			UserID:      "U1",
			TriggerID:   "trigger-" + channelID + "-" + text,
			ResponseURL: "https://hooks.slack.invalid/commands/1",
		},
	}
}

func TestHandleSlashCommand_RunsClawCommand(t *testing.T) {
	tests := []struct {
		name        string
		command     string
		text        string
		channelID   string
		wantContent string
		wantPeer    bus.Peer
	}{
		{"command in a channel", "/claw", "model", "C1", "/model", bus.Peer{Kind: "channel", ID: "C1"}},
		{"command with arguments", "/claw", "model gpt-5", "C1", "/model gpt-5", bus.Peer{Kind: "channel", ID: "C1"}},
		{"empty is help", "/claw", "  ", "C1", "/help", bus.Peer{Kind: "channel", ID: "C1"}},
		{"command in a DM", "/claw", "model", "D1", "/model", bus.Peer{Kind: "direct", ID: "U1"}},
		{"prefix already typed", "/claw", "/model", "C1", "/model", bus.Peer{Kind: "channel", ID: "C1"}},
		{"bang prefix already typed", "/claw", "!model", "C1", "/model", bus.Peer{Kind: "channel", ID: "C1"}},
		{"another launcher name", "/alice", "status", "C1", "/status", bus.Peer{Kind: "channel", ID: "C1"}},
		{"command registered by its own name", "/model", "0", "C1", "/model 0", bus.Peer{Kind: "channel", ID: "C1"}},
		{"own-name command without arguments", "/status", "", "C1", "/status", bus.Peer{Kind: "channel", ID: "C1"}},
		{"own-name command in a DM", "/model", "gpt-5", "D1", "/model gpt-5", bus.Peer{Kind: "direct", ID: "U1"}},
		{"alias registered by its own name", "/reset", "", "C1", "/reset", bus.Peer{Kind: "channel", ID: "C1"}},
		{"launcher with a message, not a command", "/claw", "hello there", "C1", "hello there", bus.Peer{Kind: "channel", ID: "C1"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, mb := newCommandChannel(t, `{"ok":true}`)
			c.handleSlashCommand(slashEvent(tt.command, tt.text, tt.channelID))

			msg := nextInbound(t, mb)
			if msg.Content != tt.wantContent {
				t.Errorf("content = %q, want %q", msg.Content, tt.wantContent)
			}
			if msg.Peer != tt.wantPeer {
				t.Errorf("peer = %+v, want %+v", msg.Peer, tt.wantPeer)
			}
			if msg.ChatID != tt.channelID {
				t.Errorf("chat id = %q, want %q", msg.ChatID, tt.channelID)
			}
		})
	}
}

// Commands typed into a message keep working: a leading space gets "/model"
// past Slack's own slash command handling, and "!" needs none.
func TestHandleMessageEvent_TypedCommands(t *testing.T) {
	for _, tt := range []struct{ text, want string }{
		{" /model", "/model"},
		{"!model", "!model"},
	} {
		t.Run(tt.text, func(t *testing.T) {
			c, mb := newCommandChannel(t, `{"ok":true}`)
			c.handleMessageEvent(&slackevents.MessageEvent{
				User: "U1", Channel: "D1", Text: tt.text, TimeStamp: "1700000000.000100",
			})
			msg := nextInbound(t, mb)
			if msg.Content != tt.want {
				t.Errorf("content = %q, want %q", msg.Content, tt.want)
			}
			if msg.Peer != (bus.Peer{Kind: "direct", ID: "U1"}) {
				t.Errorf("peer = %+v, want direct U1", msg.Peer)
			}
		})
	}
}

// responseURLRecorder is a fake slash command response_url.
type responseURLRecorder struct {
	mu     sync.Mutex
	bodies []slack.WebhookMessage
}

func (r *responseURLRecorder) serve(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		var m slack.WebhookMessage
		if err := json.NewDecoder(req.Body).Decode(&m); err != nil {
			t.Errorf("decode response_url body: %v", err)
		}
		r.mu.Lock()
		r.bodies = append(r.bodies, m)
		r.mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func (r *responseURLRecorder) got() []slack.WebhookMessage {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]slack.WebhookMessage(nil), r.bodies...)
}

// When the app is not in the channel, a slash command's reply reaches the
// user privately through the command's response_url, for as long as Slack
// accepts it; any other send there still fails as not found.
func TestSend_NotInChannelUsesResponseURL(t *testing.T) {
	c, mb := newCommandChannel(t, `{"ok":false,"error":"not_in_channel"}`)
	fake := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	c.clock = fake
	rec := &responseURLRecorder{}
	srv := rec.serve(t)

	ev := slashEvent("/claw", "model", "C1")
	cmd, ok := ev.Data.(slack.SlashCommand)
	if !ok {
		t.Fatal("slash event carries no SlashCommand")
	}
	cmd.ResponseURL = srv.URL
	ev.Data = cmd
	c.handleSlashCommand(ev)
	in := nextInbound(t, mb)

	reply := bus.OutboundMessage{Channel: "slack", ChatID: in.ChatID, Content: "Model: gpt-5", OriginalMessageID: in.MessageID}
	if err := c.Send(context.Background(), reply); err != nil {
		t.Fatalf("Send = %v, want delivery through the response URL", err)
	}
	got := rec.got()
	if len(got) != 1 || got[0].Text != "Model: gpt-5" || got[0].ResponseType != slack.ResponseTypeEphemeral {
		t.Fatalf("response_url posts = %+v, want one ephemeral reply", got)
	}

	other := bus.OutboundMessage{Channel: "slack", ChatID: "C1", Content: "scheduled"}
	if err := c.Send(context.Background(), other); !errors.Is(err, channels.ErrRecipientNotFound) {
		t.Fatalf("unrelated Send = %v, want ErrRecipientNotFound", err)
	}

	fake.Advance(slashResponseTTL)
	if err := c.Send(context.Background(), reply); !errors.Is(err, channels.ErrRecipientNotFound) {
		t.Fatalf("Send after the response URL expired = %v, want ErrRecipientNotFound", err)
	}
	if n := len(rec.got()); n != 1 {
		t.Fatalf("expired response URL was used: %d posts", n)
	}
}

// A slash command has no message, so no reaction is attempted for it.
func TestReactToMessage_SkipsSlashCommand(t *testing.T) {
	var calls int
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		calls++
		mu.Unlock()
		if _, err := io.WriteString(w, `{"ok":true}`); err != nil {
			t.Errorf("write response: %v", err)
		}
	}))
	t.Cleanup(srv.Close)
	c, _ := newCommandChannel(t, `{"ok":true}`)
	c.api = slack.New("xoxb-test", slack.OptionAPIURL(srv.URL+"/"))
	c.rememberSlashResponse("trigger-1", "https://hooks.slack.invalid/commands/1")

	if _, err := c.ReactToMessage(context.Background(), "C1", "trigger-1"); err != nil {
		t.Fatalf("ReactToMessage: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if calls != 0 {
		t.Fatalf("reaction API called %d times for a slash command", calls)
	}
}
