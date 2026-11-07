package slack

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/slack-go/slack"
	"github.com/slack-go/slack/slackevents"

	"github.com/PivotLLM/ClawEh/bus"
	"github.com/PivotLLM/ClawEh/channels"
	"github.com/PivotLLM/ClawEh/config"
)

// slackCall is one Web API request the fake Slack received.
type slackCall struct {
	method string // "chat.postMessage", "chat.update", ...
	form   url.Values
}

// fakeSlackAPI records Web API calls and answers each method with replies[method]
// (default {"ok":true}).
type fakeSlackAPI struct {
	mu      sync.Mutex
	calls   []slackCall
	replies map[string]string
}

func (f *fakeSlackAPI) serve(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Errorf("parse form: %v", err)
		}
		method := strings.TrimPrefix(r.URL.Path, "/")
		f.mu.Lock()
		f.calls = append(f.calls, slackCall{method: method, form: r.PostForm})
		reply, ok := f.replies[method]
		f.mu.Unlock()
		if !ok {
			reply = `{"ok":true}`
		}
		w.Header().Set("Content-Type", "application/json")
		if _, err := io.WriteString(w, reply); err != nil {
			t.Errorf("write response: %v", err)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func (f *fakeSlackAPI) callsTo(method string) []slackCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []slackCall
	for _, c := range f.calls {
		if c.method == method {
			out = append(out, c)
		}
	}
	return out
}

func newPlaceholderChannel(t *testing.T, ph config.PlaceholderConfig, replies map[string]string) (*SlackChannel, *fakeSlackAPI) {
	t.Helper()
	api := &fakeSlackAPI{replies: replies}
	srv := api.serve(t)
	cfg := config.SlackConfig{Placeholder: ph}
	mb := bus.NewMessageBus()
	t.Cleanup(mb.Close)
	c := &SlackChannel{
		BaseChannel: channels.NewBaseChannel("slack", cfg, mb, []string{"*"}),
		config:      cfg,
		api:         slack.New("xoxb-test", slack.OptionAPIURL(srv.URL+"/")),
		botUserID:   "UBOT",
	}
	c.SetOwner(c)
	c.userNameCache.Store("U1", slackUserName{displayName: "Alice"})
	c.ctx, c.cancel = context.WithCancel(context.Background())
	t.Cleanup(c.cancel)
	c.SetRunning(true)
	return c, api
}

const postedPlaceholder = `{"ok":true,"channel":"C1","ts":"1700000001.000200"}`

// With the placeholder off (the config default) nothing is posted.
func TestSendPlaceholder_Disabled(t *testing.T) {
	c, api := newPlaceholderChannel(t, config.PlaceholderConfig{}, nil)
	id, err := c.SendPlaceholder(context.Background(), "C1")
	if err != nil || id != "" {
		t.Fatalf("SendPlaceholder = %q, %v; want nothing", id, err)
	}
	if len(api.callsTo("chat.postMessage")) != 0 {
		t.Fatal("a placeholder was posted while disabled")
	}
}

// The placeholder goes where the reply will: the thread when the chat is one,
// else the conversation. Its id is the message's ts.
func TestSendPlaceholder_PostsWhereTheReplyGoes(t *testing.T) {
	tests := []struct {
		name, chatID, text, wantText, wantThread string
	}{
		{"conversation, default text", "C1", "", defaultPlaceholderText, ""},
		{"thread, configured text", "C1/1700000000.000100", "Working on it", "Working on it", "1700000000.000100"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, api := newPlaceholderChannel(t, config.PlaceholderConfig{Enabled: true, Text: tt.text},
				map[string]string{"chat.postMessage": postedPlaceholder})
			id, err := c.SendPlaceholder(context.Background(), tt.chatID)
			if err != nil {
				t.Fatalf("SendPlaceholder: %v", err)
			}
			if id != "1700000001.000200" {
				t.Fatalf("placeholder id = %q, want the posted ts", id)
			}
			posts := api.callsTo("chat.postMessage")
			if len(posts) != 1 {
				t.Fatalf("posted %d messages, want 1", len(posts))
			}
			f := posts[0].form
			if f.Get("channel") != "C1" || f.Get("text") != tt.wantText || f.Get("thread_ts") != tt.wantThread {
				t.Fatalf("posted channel=%q text=%q thread_ts=%q, want C1 %q %q",
					f.Get("channel"), f.Get("text"), f.Get("thread_ts"), tt.wantText, tt.wantThread)
			}
		})
	}
}

// A placeholder Slack refuses is classified like a failed send.
func TestSendPlaceholder_Refused(t *testing.T) {
	c, _ := newPlaceholderChannel(t, config.PlaceholderConfig{Enabled: true},
		map[string]string{"chat.postMessage": `{"ok":false,"error":"not_in_channel"}`})
	if _, err := c.SendPlaceholder(context.Background(), "C1"); !errors.Is(err, channels.ErrRecipientNotFound) {
		t.Fatalf("SendPlaceholder = %v, want ErrRecipientNotFound", err)
	}
}

// A progress update edits the placeholder in its conversation, in Slack's
// formatting.
func TestEditMessage_UpdatesPlaceholder(t *testing.T) {
	c, api := newPlaceholderChannel(t, config.PlaceholderConfig{Enabled: true}, nil)
	if err := c.EditMessage(context.Background(), "C1/1700000000.000100", "1700000001.000200", "**Still** working"); err != nil {
		t.Fatalf("EditMessage: %v", err)
	}
	updates := api.callsTo("chat.update")
	if len(updates) != 1 {
		t.Fatalf("chat.update called %d times, want 1", len(updates))
	}
	f := updates[0].form
	if f.Get("channel") != "C1" || f.Get("ts") != "1700000001.000200" || f.Get("text") != "*Still* working" {
		t.Fatalf("update channel=%q ts=%q text=%q", f.Get("channel"), f.Get("ts"), f.Get("text"))
	}

	c2, _ := newPlaceholderChannel(t, config.PlaceholderConfig{Enabled: true},
		map[string]string{"chat.update": `{"ok":false,"error":"internal_error"}`})
	if err := c2.EditMessage(context.Background(), "C1", "1", "x"); !errors.Is(err, channels.ErrTemporary) {
		t.Fatalf("failed edit = %v, want ErrTemporary", err)
	}
}

// A placeholder whose turn sent no reply is deleted.
func TestDeleteMessage_RemovesPlaceholder(t *testing.T) {
	c, api := newPlaceholderChannel(t, config.PlaceholderConfig{Enabled: true}, nil)
	if err := c.DeleteMessage(context.Background(), "C1/1700000000.000100", "1700000001.000200"); err != nil {
		t.Fatalf("DeleteMessage: %v", err)
	}
	deletes := api.callsTo("chat.delete")
	if len(deletes) != 1 || deletes[0].form.Get("channel") != "C1" || deletes[0].form.Get("ts") != "1700000001.000200" {
		t.Fatalf("chat.delete calls = %+v", deletes)
	}
}

// placeholderRecorder records what the channel registers on inbound.
type placeholderRecorder struct {
	mu           sync.Mutex
	placeholders []string // "chatID placeholderID"
}

func (r *placeholderRecorder) RecordPlaceholder(_, chatID, placeholderID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.placeholders = append(r.placeholders, chatID+" "+placeholderID)
}
func (r *placeholderRecorder) RecordTypingStop(string, string, func())           {}
func (r *placeholderRecorder) RecordReactionUndo(string, string, string, func()) {}

// An inbound message posts the placeholder in its thread and records it for
// the reply; the reply then replaces it and acknowledges the message.
func TestInbound_PlaceholderThenReplyReplacesIt(t *testing.T) {
	c, api := newPlaceholderChannel(t, config.PlaceholderConfig{Enabled: true},
		map[string]string{"chat.postMessage": postedPlaceholder})
	rec := &placeholderRecorder{}
	c.SetPlaceholderRecorder(rec)

	c.handleMessageEvent(&slackevents.MessageEvent{
		User: "U1", Channel: "D1", Text: "hello", TimeStamp: "1700000000.000300", ThreadTimeStamp: "1700000000.000100",
	})
	rec.mu.Lock()
	got := append([]string(nil), rec.placeholders...)
	rec.mu.Unlock()
	if len(got) != 1 || got[0] != "D1/1700000000.000100 1700000001.000200" {
		t.Fatalf("recorded placeholders = %v", got)
	}
	if posts := api.callsTo("chat.postMessage"); len(posts) != 1 || posts[0].form.Get("thread_ts") != "1700000000.000100" {
		t.Fatalf("placeholder posts = %+v, want one in the thread", posts)
	}

	err := c.ReplacePlaceholder(context.Background(), "1700000001.000200", bus.OutboundMessage{
		Channel: "slack", ChatID: "D1/1700000000.000100", OriginalMessageID: "1700000000.000300", Content: "the reply",
	})
	if err != nil {
		t.Fatalf("ReplacePlaceholder: %v", err)
	}
	if updates := api.callsTo("chat.update"); len(updates) != 1 || updates[0].form.Get("text") != "the reply" {
		t.Fatalf("chat.update calls = %+v, want the reply", updates)
	}
	waitFor(t, "the check mark on the answered message", func() bool {
		for _, r := range api.callsTo("reactions.add") {
			if r.form.Get("name") == "white_check_mark" && r.form.Get("timestamp") == "1700000000.000300" {
				return true
			}
		}
		return false
	})
	if _, pending := c.pendingAcks.Load("1700000000.000300"); pending {
		t.Fatal("message still waiting for its acknowledgement")
	}
}
