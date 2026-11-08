package discord

import (
	"context"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/bwmarrin/discordgo"

	"github.com/PivotLLM/ClawEh/bus"
	"github.com/PivotLLM/ClawEh/channels"
	"github.com/PivotLLM/ClawEh/channels/internal/fdcheck"
	"github.com/PivotLLM/ClawEh/media"
)

// discordUpload is one message Discord received: its path and the name and
// body of every file part.
type discordUpload struct {
	path  string
	files map[string]string
}

// newDiscordMediaChannel points discordgo at a stub that records each
// message's files and answers status (200 when 0).
func newDiscordMediaChannel(t *testing.T, status int) (*DiscordChannel, *media.FileMediaStore, *[]discordUpload, *sync.Mutex) {
	t.Helper()
	var mu sync.Mutex
	var got []discordUpload
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		up := discordUpload{path: r.URL.Path, files: map[string]string{}}
		_, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err == nil && params["boundary"] != "" {
			mr := multipart.NewReader(r.Body, params["boundary"])
			for {
				p, err := mr.NextPart()
				if err != nil {
					break
				}
				body, err := io.ReadAll(p)
				if err != nil {
					t.Errorf("read part: %v", err)
				}
				if p.FileName() != "" {
					up.files[p.FileName()] = string(body)
				}
			}
		}
		mu.Lock()
		got = append(got, up)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		reply := `{"id":"1","channel_id":"2"}`
		if status != 0 {
			w.WriteHeader(status)
			reply = `{"code":50013,"message":"Missing Permissions"}`
		}
		if _, err := io.WriteString(w, reply); err != nil {
			t.Errorf("write reply: %v", err)
		}
	}))
	t.Cleanup(srv.Close)
	orig := discordgo.EndpointChannels
	discordgo.EndpointChannels = srv.URL + "/channels/"
	t.Cleanup(func() { discordgo.EndpointChannels = orig })

	session, err := discordgo.New("Bot test")
	if err != nil {
		t.Fatal(err)
	}
	session.MaxRestRetries = 0
	c := &DiscordChannel{BaseChannel: channels.NewBaseChannel("discord", nil, nil, nil), session: session}
	store := media.NewFileMediaStore()
	c.SetMediaStore(store)
	c.SetRunning(true)
	return c, store, &got, &mu
}

func storeDiscordFile(t *testing.T, store *media.FileMediaStore, name, body string) (string, string) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "staged-"+name)
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	ref, err := store.Store(p, media.MediaMeta{Filename: name}, "test")
	if err != nil {
		t.Fatal(err)
	}
	return ref, p
}

// All parts go in one message to the chat (a thread is its own channel id),
// each under the part's file name, and no file stays open.
func TestSendMedia_AllPartsInOneMessage(t *testing.T) {
	c, store, got, mu := newDiscordMediaChannel(t, 0)
	ref1, p1 := storeDiscordFile(t, store, "a.txt", "alpha")
	ref2, p2 := storeDiscordFile(t, store, "b.png", "beta")

	err := c.SendMedia(context.Background(), bus.OutboundMediaMessage{
		ChatID: "999",
		Parts: []bus.MediaPart{
			{Type: "file", Ref: ref1, Filename: "report.txt", Caption: "here"},
			{Type: "image", Ref: ref2},
		},
	})
	if err != nil {
		t.Fatalf("SendMedia = %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(*got) != 1 {
		t.Fatalf("messages = %d, want 1", len(*got))
	}
	m := (*got)[0]
	if !strings.Contains(m.path, "/channels/999/messages") {
		t.Errorf("path = %q, want the chat's messages", m.path)
	}
	if m.files["report.txt"] != "alpha" || m.files["file"] != "beta" {
		t.Errorf("files = %v, want report.txt=alpha and file=beta", m.files)
	}
	for _, p := range []string{p1, p2} {
		if open, ok := fdcheck.OpenFor(p); ok && open {
			t.Errorf("%s still open after the send", p)
		}
	}
}

// A part that is gone fails the send naming it, nothing is posted, and the
// files opened for the earlier parts are closed.
func TestSendMedia_LaterPartGoneClosesEarlierFiles(t *testing.T) {
	c, store, got, mu := newDiscordMediaChannel(t, 0)
	ref1, p1 := storeDiscordFile(t, store, "a.txt", "alpha")
	ref2, p2 := storeDiscordFile(t, store, "b.txt", "beta")
	if err := os.Remove(p2); err != nil {
		t.Fatal(err)
	}

	err := c.SendMedia(context.Background(), bus.OutboundMediaMessage{
		ChatID: "999",
		Parts:  []bus.MediaPart{{Type: "file", Ref: ref1}, {Type: "file", Ref: ref2, Filename: "b.txt"}},
	})
	if !errors.Is(err, channels.ErrSendFailed) || !strings.Contains(err.Error(), "b.txt") {
		t.Fatalf("SendMedia = %v, want ErrSendFailed naming b.txt", err)
	}
	mu.Lock()
	n := len(*got)
	mu.Unlock()
	if n != 0 {
		t.Fatalf("messages = %d, want none", n)
	}
	if open, ok := fdcheck.OpenFor(p1); ok && open {
		t.Errorf("first part's file left open")
	}
}

// Discord refusing the message is reported, and the files are closed.
func TestSendMedia_RefusedClosesFiles(t *testing.T) {
	c, store, _, _ := newDiscordMediaChannel(t, http.StatusForbidden)
	ref, p := storeDiscordFile(t, store, "a.txt", "alpha")
	err := c.SendMedia(context.Background(), bus.OutboundMediaMessage{ChatID: "999", Parts: []bus.MediaPart{{Type: "file", Ref: ref}}})
	if err == nil {
		t.Fatal("SendMedia = nil, want an error")
	}
	if open, ok := fdcheck.OpenFor(p); ok && open {
		t.Errorf("file left open after a refused send")
	}
}

// The refusals before any part.
func TestSendMedia_DiscordPreconditions(t *testing.T) {
	c, store, got, mu := newDiscordMediaChannel(t, 0)
	ref, _ := storeDiscordFile(t, store, "a.txt", "alpha")
	parts := []bus.MediaPart{{Type: "file", Ref: ref}}

	if err := c.SendMedia(context.Background(), bus.OutboundMediaMessage{ChatID: "", Parts: parts}); err == nil {
		t.Error("empty chat: nil error")
	}
	if err := c.SendMedia(context.Background(), bus.OutboundMediaMessage{ChatID: "999"}); err != nil {
		t.Errorf("no parts: %v, want nil", err)
	}
	c.SetRunning(false)
	if err := c.SendMedia(context.Background(), bus.OutboundMediaMessage{ChatID: "999", Parts: parts}); !errors.Is(err, channels.ErrNotRunning) {
		t.Errorf("stopped: %v, want ErrNotRunning", err)
	}
	c.SetRunning(true)
	c.SetMediaStore(nil)
	if err := c.SendMedia(context.Background(), bus.OutboundMediaMessage{ChatID: "999", Parts: parts}); !errors.Is(err, channels.ErrSendFailed) {
		t.Errorf("no store: %v, want ErrSendFailed", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(*got) != 0 {
		t.Errorf("messages = %d, want none", len(*got))
	}
}
