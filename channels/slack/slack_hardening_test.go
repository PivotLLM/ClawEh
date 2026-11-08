package slack

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/slack-go/slack"

	"github.com/PivotLLM/ClawEh/bus"
	"github.com/PivotLLM/ClawEh/channels"
	"github.com/PivotLLM/ClawEh/config"
	"github.com/PivotLLM/ClawEh/media"
)

// newMediaChannelOn returns a running Slack channel on a fakeSlackAPI that
// answers replies, with an empty media store.
func newMediaChannelOn(t *testing.T, replies map[string]string) (*SlackChannel, *fakeSlackAPI, *media.FileMediaStore) {
	t.Helper()
	api := &fakeSlackAPI{replies: replies}
	srv := api.serve(t)
	if replies == nil {
		api.replies = map[string]string{}
	}
	if _, ok := api.replies["files.getUploadURLExternal"]; !ok {
		api.replies["files.getUploadURLExternal"] = `{"ok":true,"upload_url":"` + srv.URL + `/upload","file_id":"F1"}`
	}
	c := &SlackChannel{
		BaseChannel: channels.NewBaseChannel("slack", nil, nil, nil),
		api:         slack.New("xoxb-test", slack.OptionAPIURL(srv.URL+"/")),
	}
	c.SetRunning(true)
	store := media.NewFileMediaStore()
	c.SetMediaStore(store)
	return c, api, store
}

func storeSlackFile(t *testing.T, store *media.FileMediaStore, name string) (string, string) {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte("data of "+name), 0o600); err != nil {
		t.Fatal(err)
	}
	ref, err := store.Store(p, media.MediaMeta{Filename: name}, "test")
	if err != nil {
		t.Fatal(err)
	}
	return ref, p
}

// Every part is uploaded and shared in order, titled by its caption or name,
// in the conversation when the chat is not a thread.
func TestSendMedia_EveryPartInOrder(t *testing.T) {
	c, api, store := newMediaChannelOn(t, nil)
	ref1, _ := storeSlackFile(t, store, "a.txt")
	ref2, _ := storeSlackFile(t, store, "b.txt")
	err := c.SendMedia(context.Background(), bus.OutboundMediaMessage{
		ChatID: "C123",
		Parts: []bus.MediaPart{
			{Type: "file", Ref: ref1, Filename: "a.txt", Caption: "First"},
			{Type: "file", Ref: ref2},
		},
	})
	if err != nil {
		t.Fatalf("SendMedia = %v", err)
	}
	gets := api.callsTo("files.getUploadURLExternal")
	completes := api.callsTo("files.completeUploadExternal")
	if len(gets) != 2 || len(completes) != 2 || len(api.callsTo("upload")) != 2 {
		t.Fatalf("calls = %+v", api.calls)
	}
	if gets[0].form.Get("filename") != "a.txt" || gets[1].form.Get("filename") != "file" {
		t.Errorf("names = %q, %q; want a.txt and the default", gets[0].form.Get("filename"), gets[1].form.Get("filename"))
	}
	for _, cpl := range completes {
		if cpl.form.Get("channel_id") != "C123" || cpl.form.Get("thread_ts") != "" {
			t.Errorf("complete channel=%q thread=%q, want C123 and no thread", cpl.form.Get("channel_id"), cpl.form.Get("thread_ts"))
		}
	}
	if !strings.Contains(completes[0].form.Get("files"), `"First"`) || !strings.Contains(completes[1].form.Get("files"), `"file"`) {
		t.Errorf("titles = %s / %s", completes[0].form.Get("files"), completes[1].form.Get("files"))
	}
}

// A file that disappears between staging and sending fails the send without
// a retry, naming it.
func TestSendMedia_FileGoneAfterResolveFails(t *testing.T) {
	c, api, store := newMediaChannelOn(t, nil)
	ref, p := storeSlackFile(t, store, "gone.txt")
	if err := os.Remove(p); err != nil {
		t.Fatal(err)
	}
	err := c.SendMedia(context.Background(), bus.OutboundMediaMessage{
		ChatID: "C123", Parts: []bus.MediaPart{{Type: "file", Ref: ref, Filename: "gone.txt"}},
	})
	if !errors.Is(err, channels.ErrSendFailed) || errors.Is(err, channels.ErrTemporary) || !strings.Contains(err.Error(), "gone.txt") {
		t.Fatalf("SendMedia = %v, want ErrSendFailed (not temporary) naming gone.txt", err)
	}
	if len(api.callsTo("files.getUploadURLExternal")) != 0 {
		t.Fatal("Slack was asked to upload a file that is gone")
	}
}

// Every step of the upload says why it failed: a missing files:write scope
// at any step, a conversation the app cannot post to, else temporary.
func TestSendMedia_StepFailures(t *testing.T) {
	tests := []struct {
		name    string
		replies map[string]string
		want    error
		text    string
	}{
		{
			"complete without scope",
			map[string]string{"files.completeUploadExternal": `{"ok":false,"error":"missing_scope"}`},
			channels.ErrSendFailed, "files:write",
		},
		{
			"complete not in channel",
			map[string]string{"files.completeUploadExternal": `{"ok":false,"error":"not_in_channel"}`},
			channels.ErrRecipientNotFound, "",
		},
		{
			"get URL server error",
			map[string]string{"files.getUploadURLExternal": `{"ok":false,"error":"internal_error"}`},
			channels.ErrTemporary, "",
		},
		{
			"upload unreachable",
			map[string]string{"files.getUploadURLExternal": `{"ok":true,"upload_url":"http://127.0.0.1:1/upload","file_id":"F1"}`},
			channels.ErrTemporary, "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, _, store := newMediaChannelOn(t, tt.replies)
			ref, _ := storeSlackFile(t, store, "a.txt")
			err := c.SendMedia(context.Background(), bus.OutboundMediaMessage{
				ChatID: "C123", Parts: []bus.MediaPart{{Type: "file", Ref: ref, Filename: "a.txt"}},
			})
			if !errors.Is(err, tt.want) || (tt.text != "" && !strings.Contains(err.Error(), tt.text)) {
				t.Fatalf("SendMedia = %v, want %v %q", err, tt.want, tt.text)
			}
		})
	}
}

// The refusals before any upload.
func TestSendMedia_SlackPreconditions(t *testing.T) {
	c, api, store := newMediaChannelOn(t, nil)
	ref, _ := storeSlackFile(t, store, "a.txt")
	parts := []bus.MediaPart{{Type: "file", Ref: ref}}

	if err := c.SendMedia(context.Background(), bus.OutboundMediaMessage{ChatID: "/123", Parts: parts}); err == nil {
		t.Error("invalid chat: nil error")
	}
	c.SetRunning(false)
	if err := c.SendMedia(context.Background(), bus.OutboundMediaMessage{ChatID: "C1", Parts: parts}); !errors.Is(err, channels.ErrNotRunning) {
		t.Errorf("stopped: %v", err)
	}
	c.SetRunning(true)
	c.SetMediaStore(nil)
	if err := c.SendMedia(context.Background(), bus.OutboundMediaMessage{ChatID: "C1", Parts: parts}); !errors.Is(err, channels.ErrSendFailed) {
		t.Errorf("no store: %v", err)
	}
	if len(api.calls) != 0 {
		t.Errorf("calls = %+v, want none", api.calls)
	}
}

// A reply that cannot replace its placeholder reports the failure and does
// not acknowledge the message, so the manager falls back to a normal send,
// which does.
func TestReplacePlaceholder_FailureLeavesAckForSend(t *testing.T) {
	c, api := newPlaceholderChannel(t, config.PlaceholderConfig{Enabled: true},
		map[string]string{"chat.update": `{"ok":false,"error":"message_not_found"}`})
	c.pendingAcks.Store("1700000000.000300", slackMessageRef{ChannelID: "C1", Timestamp: "1700000000.000300"})
	msg := bus.OutboundMessage{ChatID: "C1", OriginalMessageID: "1700000000.000300", Content: "reply"}

	if err := c.ReplacePlaceholder(context.Background(), "1700000001.000200", msg); err == nil {
		t.Fatal("ReplacePlaceholder = nil on a failed edit")
	}
	if _, pending := c.pendingAcks.Load("1700000000.000300"); !pending {
		t.Fatal("a failed replacement consumed the acknowledgement")
	}
	if err := c.Send(context.Background(), msg); err != nil {
		t.Fatalf("Send: %v", err)
	}
	waitFor(t, "the check mark after the fallback send", func() bool {
		return len(api.callsTo("reactions.add")) == 1
	})
	// Acknowledged once: a second reply adds no second check mark.
	if err := c.Send(context.Background(), msg); err != nil {
		t.Fatalf("second Send: %v", err)
	}
	if n := len(api.callsTo("reactions.add")); n != 1 {
		t.Fatalf("reactions.add calls = %d, want 1", n)
	}
}

// Edits and deletes of a chat with no conversation id are refused before
// Slack is called; a refused delete is classified.
func TestEditDelete_InvalidAndRefused(t *testing.T) {
	c, api := newPlaceholderChannel(t, config.PlaceholderConfig{Enabled: true},
		map[string]string{"chat.delete": `{"ok":false,"error":"channel_not_found"}`})
	if err := c.EditMessage(context.Background(), "", "1", "x"); err == nil {
		t.Error("EditMessage on an empty chat: nil error")
	}
	if err := c.DeleteMessage(context.Background(), "/1", "1"); err == nil {
		t.Error("DeleteMessage on an empty chat: nil error")
	}
	if _, err := c.SendPlaceholder(context.Background(), ""); err == nil {
		t.Error("SendPlaceholder on an empty chat: nil error")
	}
	if len(api.calls) != 0 {
		t.Fatalf("calls = %+v, want none", api.calls)
	}
	if err := c.DeleteMessage(context.Background(), "C1", "1"); !errors.Is(err, channels.ErrRecipientNotFound) {
		t.Fatalf("refused delete = %v, want ErrRecipientNotFound", err)
	}
}
