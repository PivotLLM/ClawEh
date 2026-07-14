package secmsg

import (
	"context"
	"testing"
	"time"

	"github.com/PivotLLM/ClawEh/pkg/bus"
	"github.com/PivotLLM/ClawEh/pkg/channels"

	"github.com/tenebris-tech/secmsg/schema"
)

// newTestChannel builds a channel wired to a real bus with a fixed resolved
// service so canonical IDs are deterministic ("signal:<id>").
func newTestChannel(b *bus.MessageBus, allow []string) *SecMsgChannel {
	c := &SecMsgChannel{
		BaseChannel: channels.NewBaseChannel("signal", nil, b, allow),
		service:     "signal",
		account:     "acct",
		ctx:         context.Background(),
	}
	c.SetOwner(c)
	return c
}

// consume drains one inbound message, or reports that none arrived within the
// window (used to assert a message was dropped).
func consume(t *testing.T, b *bus.MessageBus) (bus.InboundMessage, bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	return b.ConsumeInbound(ctx)
}

func directMsg() schema.MessageParams {
	return schema.MessageParams{
		Service:   "signal",
		Account:   "acct",
		From:      schema.Party{ID: "uuid-alice", Name: "Alice"},
		To:        schema.Party{ID: "acct", Self: true},
		Type:      schema.MessageTypeText,
		Timestamp: 1000,
		Body:      "hello",
	}
}

func TestHandleMessage_Direct(t *testing.T) {
	b := bus.NewMessageBus()
	c := newTestChannel(b, []string{"*"})

	c.handleMessage(directMsg())

	in, ok := consume(t, b)
	if !ok {
		t.Fatal("expected inbound message")
	}
	if in.Channel != "signal" || in.Content != "hello" {
		t.Fatalf("channel=%q content=%q", in.Channel, in.Content)
	}
	if in.ChatID != "uuid-alice" {
		t.Fatalf("direct chatID should be the sender id, got %q", in.ChatID)
	}
	if in.Peer.Kind != "direct" || in.Peer.ID != "uuid-alice" {
		t.Fatalf("peer=%+v", in.Peer)
	}
	if in.Sender.CanonicalID != "signal:uuid-alice" || in.Sender.DisplayName != "Alice" {
		t.Fatalf("sender=%+v", in.Sender)
	}
}

func TestHandleMessage_Group(t *testing.T) {
	b := bus.NewMessageBus()
	c := newTestChannel(b, []string{"*"})

	m := directMsg()
	m.To = schema.Party{ID: "GROUPHEX"} // group: not self, carries the group id
	c.handleMessage(m)

	in, ok := consume(t, b)
	if !ok {
		t.Fatal("expected inbound message")
	}
	// Group replies must round-trip through the encoded ChatID so Send can pick
	// SendGroupMessage without a side lookup.
	if in.ChatID != groupChatPrefix+"GROUPHEX" {
		t.Fatalf("group chatID=%q", in.ChatID)
	}
	if in.Peer.Kind != "group" || in.Peer.ID != "GROUPHEX" {
		t.Fatalf("peer=%+v", in.Peer)
	}
	if id, ok := decodeGroupChatID(in.ChatID); !ok || id != "GROUPHEX" {
		t.Fatalf("group chatID did not decode: id=%q ok=%v", id, ok)
	}
}

func TestHandleMessage_DropsSyncCopiesAndNonText(t *testing.T) {
	cases := map[string]func(schema.MessageParams) schema.MessageParams{
		"our own sync copy": func(m schema.MessageParams) schema.MessageParams {
			m.From.Self = true
			return m
		},
		"reaction event": func(m schema.MessageParams) schema.MessageParams {
			m.Type = schema.MessageTypeReactionAdd
			return m
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			b := bus.NewMessageBus()
			c := newTestChannel(b, []string{"*"})
			c.handleMessage(mutate(directMsg()))
			if _, ok := consume(t, b); ok {
				t.Fatal("expected message to be dropped")
			}
		})
	}
}

func TestHandleMessage_AllowlistReject(t *testing.T) {
	b := bus.NewMessageBus()
	c := newTestChannel(b, []string{"signal:someone-else"})
	c.handleMessage(directMsg())
	if _, ok := consume(t, b); ok {
		t.Fatal("expected disallowed sender to be dropped")
	}
}

func TestHandleMessage_AttachmentWithoutStore(t *testing.T) {
	b := bus.NewMessageBus()
	c := newTestChannel(b, []string{"*"})

	m := directMsg()
	m.Body = ""
	m.Attachments = []schema.Attachment{
		{LocalPath: "/tmp/pic.jpg", FileName: "pic.jpg", ContentType: "image/jpeg", Caption: "a cat"},
	}
	c.handleMessage(m)

	in, ok := consume(t, b)
	if !ok {
		t.Fatal("expected inbound message")
	}
	// With no media store injected the raw daemon path is carried through.
	if len(in.Media) != 1 || in.Media[0] != "/tmp/pic.jpg" {
		t.Fatalf("media=%v", in.Media)
	}
	if in.Content != "a cat\n[attachment: pic.jpg]" {
		t.Fatalf("content=%q", in.Content)
	}
}

func TestAppendLine(t *testing.T) {
	if got := appendLine("", "x"); got != "x" {
		t.Fatalf("empty base: %q", got)
	}
	if got := appendLine("a", "b"); got != "a\nb" {
		t.Fatalf("join: %q", got)
	}
}

func TestAttachmentLabel(t *testing.T) {
	tests := []struct {
		a    schema.Attachment
		want string
	}{
		{schema.Attachment{FileName: "doc.pdf", ContentType: "application/pdf"}, "doc.pdf"},
		{schema.Attachment{ContentType: "image/png"}, "image/png"},
		{schema.Attachment{}, "file"},
	}
	for _, tt := range tests {
		if got := attachmentLabel(tt.a); got != tt.want {
			t.Errorf("attachmentLabel(%+v)=%q want %q", tt.a, got, tt.want)
		}
	}
}
