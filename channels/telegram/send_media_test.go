package telegram

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/PivotLLM/ClawEh/bus"
	"github.com/PivotLLM/ClawEh/channels"
	"github.com/PivotLLM/ClawEh/media"
)

// mediaRequest is what the fake Bot API saw of one upload.
type mediaRequest struct {
	method   string
	thread   string
	filename string
}

// newMediaBot returns a running channel whose Bot API records uploads and
// accepts them, with a media store holding one staged file.
func newMediaBot(t *testing.T) (*TelegramChannel, media.MediaStore, func() []mediaRequest) {
	t.Helper()
	var mu sync.Mutex
	var seen []mediaRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/getMe") {
			writeBody(t, w, getMeReply)
			return
		}
		req := mediaRequest{method: r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]}
		if err := r.ParseMultipartForm(1 << 20); err == nil {
			req.thread = r.FormValue("message_thread_id")
			for _, files := range r.MultipartForm.File {
				req.filename = files[0].Filename
			}
		}
		mu.Lock()
		seen = append(seen, req)
		mu.Unlock()
		writeBody(t, w, `{"ok":true,"result":{"message_id":1,"date":0,"chat":{"id":12345,"type":"private"}}}`)
	}))
	t.Cleanup(srv.Close)
	ch := newFakeBotChannel(t, srv)
	store := media.NewFileMediaStore()
	ch.SetMediaStore(store)
	return ch, store, func() []mediaRequest {
		mu.Lock()
		defer mu.Unlock()
		return append([]mediaRequest(nil), seen...)
	}
}

// A file goes to the chat's topic under the name the part gives, not the
// staged file's name; an image goes as a photo, anything else as a document.
func TestSendMedia_TopicNameAndKind(t *testing.T) {
	ch, store, seen := newMediaBot(t)
	path := filepath.Join(t.TempDir(), "send-123-staged.bin")
	require.NoError(t, os.WriteFile(path, []byte("# notes"), 0o600))
	ref, err := store.Store(path, media.MediaMeta{Filename: "notes.md"}, "test")
	require.NoError(t, err)

	require.NoError(t, ch.SendMedia(context.Background(), bus.OutboundMediaMessage{
		Channel: "telegram", ChatID: "12345/7",
		Parts: []bus.MediaPart{
			{Type: "file", Ref: ref, Filename: "notes.md"},
			{Type: "image", Ref: ref, Filename: "chart.png"},
		},
	}))
	got := seen()
	require.Len(t, got, 2)
	require.Equal(t, mediaRequest{method: "sendDocument", thread: "7", filename: "notes.md"}, got[0])
	require.Equal(t, mediaRequest{method: "sendPhoto", thread: "7", filename: "chart.png"}, got[1])
}

// A part whose file is gone fails the send instead of being skipped, so the
// sender never takes it for delivered.
func TestSendMedia_MissingFileFails(t *testing.T) {
	ch, store, seen := newMediaBot(t)
	err := ch.SendMedia(context.Background(), bus.OutboundMediaMessage{
		Channel: "telegram", ChatID: "12345",
		Parts: []bus.MediaPart{{Type: "file", Ref: "media://missing", Filename: "notes.md"}},
	})
	require.True(t, errors.Is(err, channels.ErrSendFailed), "SendMedia = %v, want ErrSendFailed", err)
	require.Contains(t, err.Error(), "notes.md")
	require.Empty(t, seen())

	path := filepath.Join(t.TempDir(), "gone.md")
	require.NoError(t, os.WriteFile(path, []byte("x"), 0o600))
	ref, err := store.Store(path, media.MediaMeta{Filename: "gone.md"}, "test")
	require.NoError(t, err)
	require.NoError(t, os.Remove(path))
	err = ch.SendMedia(context.Background(), bus.OutboundMediaMessage{
		Channel: "telegram", ChatID: "12345",
		Parts: []bus.MediaPart{{Type: "file", Ref: ref, Filename: "gone.md"}},
	})
	require.True(t, errors.Is(err, channels.ErrSendFailed), "SendMedia = %v, want ErrSendFailed", err)
	require.Empty(t, seen())
}
