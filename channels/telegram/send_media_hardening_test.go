package telegram

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/PivotLLM/ClawEh/bus"
	"github.com/PivotLLM/ClawEh/channels"
	"github.com/PivotLLM/ClawEh/channels/internal/fdcheck"
	"github.com/PivotLLM/ClawEh/media"
)

// Audio and video go by their own methods to the chat (no topic: no thread
// id), a part without a name keeps the staged file's, and every file opened
// for the send is closed again.
func TestSendMedia_AudioVideoAndFilesClosed(t *testing.T) {
	ch, store, seen := newMediaBot(t)
	path := filepath.Join(t.TempDir(), "staged.bin")
	require.NoError(t, os.WriteFile(path, []byte("data"), 0o600))
	ref, err := store.Store(path, media.MediaMeta{Filename: "x"}, "test")
	require.NoError(t, err)

	require.NoError(t, ch.SendMedia(context.Background(), bus.OutboundMediaMessage{
		ChatID: "12345",
		Parts: []bus.MediaPart{
			{Type: "audio", Ref: ref, Filename: "song.mp3"},
			{Type: "video", Ref: ref},
		},
	}))
	got := seen()
	require.Len(t, got, 2)
	require.Equal(t, mediaRequest{method: "sendAudio", filename: "song.mp3"}, got[0])
	require.Equal(t, mediaRequest{method: "sendVideo", filename: "staged.bin"}, got[1])
	if open, ok := fdcheck.OpenFor(path); ok && open {
		t.Fatal("staged file left open after the send")
	}
}

// A part that cannot be opened fails the send, naming it, and nothing after
// it is sent.
func TestSendMedia_UnopenablePartFails(t *testing.T) {
	ch, store, seen := newMediaBot(t)
	dir := t.TempDir()
	bad := filepath.Join(dir, "bad.txt")
	require.NoError(t, os.WriteFile(bad, []byte("x"), 0o600))
	badRef, err := store.Store(bad, media.MediaMeta{Filename: "bad.txt"}, "test")
	require.NoError(t, err)
	// Replace the file with a dangling link: it resolves but cannot be opened.
	require.NoError(t, os.Remove(bad))
	require.NoError(t, os.Symlink(filepath.Join(dir, "nowhere"), bad))
	good := filepath.Join(dir, "good.txt")
	require.NoError(t, os.WriteFile(good, []byte("y"), 0o600))
	goodRef, err := store.Store(good, media.MediaMeta{Filename: "good.txt"}, "test")
	require.NoError(t, err)

	err = ch.SendMedia(context.Background(), bus.OutboundMediaMessage{
		ChatID: "12345",
		Parts:  []bus.MediaPart{{Type: "file", Ref: badRef, Filename: "bad.txt"}, {Type: "file", Ref: goodRef}},
	})
	require.True(t, errors.Is(err, channels.ErrSendFailed), "SendMedia = %v", err)
	require.Contains(t, err.Error(), "bad.txt")
	require.Empty(t, seen())
}

// The refusals before any part.
func TestSendMedia_TelegramPreconditions(t *testing.T) {
	ch, _, seen := newMediaBot(t)
	parts := []bus.MediaPart{{Type: "file", Ref: "media://x"}}

	err := ch.SendMedia(context.Background(), bus.OutboundMediaMessage{ChatID: "not-a-chat", Parts: parts})
	require.True(t, errors.Is(err, channels.ErrSendFailed), "invalid chat: %v", err)

	ch.SetMediaStore(nil)
	err = ch.SendMedia(context.Background(), bus.OutboundMediaMessage{ChatID: "12345", Parts: parts})
	require.True(t, errors.Is(err, channels.ErrSendFailed), "no store: %v", err)

	ch.SetRunning(false)
	err = ch.SendMedia(context.Background(), bus.OutboundMediaMessage{ChatID: "12345", Parts: parts})
	require.True(t, errors.Is(err, channels.ErrNotRunning), "stopped: %v", err)
	require.Empty(t, seen())
}
