package matrix

import (
	"context"
	"errors"
	"testing"

	"maunium.net/go/mautrix"

	"github.com/PivotLLM/ClawEh/bus"
	"github.com/PivotLLM/ClawEh/channels"
	"github.com/PivotLLM/ClawEh/media"
)

// A part whose file is gone fails the send instead of being skipped, so the
// sender never takes it for delivered.
func TestSendMedia_MissingFileFails(t *testing.T) {
	c := &MatrixChannel{BaseChannel: channels.NewBaseChannel("matrix", nil, nil, nil), client: &mautrix.Client{}}
	c.SetMediaStore(media.NewFileMediaStore())
	c.SetRunning(true)

	err := c.SendMedia(context.Background(), bus.OutboundMediaMessage{
		Channel: "matrix", ChatID: "!room:matrix.test",
		Parts: []bus.MediaPart{{Type: "file", Ref: "media://missing", Filename: "a.txt"}},
	})
	if !errors.Is(err, channels.ErrSendFailed) {
		t.Fatalf("SendMedia = %v, want ErrSendFailed", err)
	}
}
