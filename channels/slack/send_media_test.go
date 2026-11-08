package slack

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/slack-go/slack"

	"github.com/PivotLLM/ClawEh/bus"
	"github.com/PivotLLM/ClawEh/channels"
	"github.com/PivotLLM/ClawEh/media"
)

// fakeFilesAPI is a Slack Web API that takes file uploads; uploadURLBody,
// when set, is its answer to files.getUploadURLExternal.
type fakeFilesAPI struct {
	mu            sync.Mutex
	calls         []string
	completeForm  map[string]string
	uploadURLBody string
}

func (f *fakeFilesAPI) serve(t *testing.T) *httptest.Server {
	t.Helper()
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Errorf("parse form: %v", err)
		}
		method := strings.TrimPrefix(r.URL.Path, "/")
		f.mu.Lock()
		f.calls = append(f.calls, method)
		if method == "files.completeUploadExternal" {
			f.completeForm = map[string]string{"channel_id": r.FormValue("channel_id"), "thread_ts": r.FormValue("thread_ts")}
		}
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		body := `{"ok":true}`
		switch method {
		case "files.getUploadURLExternal":
			body = `{"ok":true,"upload_url":"` + srv.URL + `/upload","file_id":"F1"}`
			if f.uploadURLBody != "" {
				body = f.uploadURLBody
			}
		case "files.completeUploadExternal":
			body = `{"ok":true,"files":[{"id":"F1","title":"notes.md"}]}`
		}
		if _, err := io.WriteString(w, body); err != nil {
			t.Errorf("write response: %v", err)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// newMediaChannel returns a running Slack channel on srv with a media store
// holding one file, and that file's ref.
func newMediaChannel(t *testing.T, srv *httptest.Server) (*SlackChannel, string) {
	t.Helper()
	c := &SlackChannel{
		BaseChannel: channels.NewBaseChannel("slack", nil, nil, nil),
		api:         slack.New("xoxb-test", slack.OptionAPIURL(srv.URL+"/")),
	}
	c.SetRunning(true)
	store := media.NewFileMediaStore()
	c.SetMediaStore(store)
	path := filepath.Join(t.TempDir(), "notes.md")
	if err := os.WriteFile(path, []byte("# notes"), 0o600); err != nil {
		t.Fatal(err)
	}
	ref, err := store.Store(path, media.MediaMeta{Filename: "notes.md"}, "test")
	if err != nil {
		t.Fatal(err)
	}
	return c, ref
}

// A file sent to a thread lands in that thread, where the text reply goes.
func TestSendMedia_GoesToThread(t *testing.T) {
	api := &fakeFilesAPI{}
	c, ref := newMediaChannel(t, api.serve(t))
	err := c.SendMedia(context.Background(), bus.OutboundMediaMessage{
		Channel: "slack", ChatID: "C123/1700000000.000100",
		Parts: []bus.MediaPart{{Type: "file", Ref: ref, Filename: "notes.md"}},
	})
	if err != nil {
		t.Fatalf("SendMedia = %v", err)
	}
	want := map[string]string{"channel_id": "C123", "thread_ts": "1700000000.000100"}
	if api.completeForm["channel_id"] != want["channel_id"] || api.completeForm["thread_ts"] != want["thread_ts"] {
		t.Fatalf("completeUploadExternal form = %v, want %v", api.completeForm, want)
	}
}

// A Bot Token without files:write is named in the failure, which is not
// retried.
func TestSendMedia_MissingFilesScope(t *testing.T) {
	for _, code := range []string{"missing_scope", "not_allowed_token_type"} {
		t.Run(code, func(t *testing.T) {
			api := &fakeFilesAPI{uploadURLBody: `{"ok":false,"error":"` + code + `"}`}
			c, ref := newMediaChannel(t, api.serve(t))
			err := c.SendMedia(context.Background(), bus.OutboundMediaMessage{
				Channel: "slack", ChatID: "C123",
				Parts: []bus.MediaPart{{Type: "file", Ref: ref, Filename: "notes.md"}},
			})
			if !errors.Is(err, channels.ErrSendFailed) || !strings.Contains(err.Error(), "missing the files:write permission") {
				t.Fatalf("SendMedia = %v, want ErrSendFailed naming files:write", err)
			}
		})
	}
}

// A part whose file is gone fails the send instead of being skipped.
func TestSendMedia_MissingFileFails(t *testing.T) {
	api := &fakeFilesAPI{}
	c, _ := newMediaChannel(t, api.serve(t))
	err := c.SendMedia(context.Background(), bus.OutboundMediaMessage{
		Channel: "slack", ChatID: "C123",
		Parts: []bus.MediaPart{{Type: "file", Ref: "media://missing", Filename: "notes.md"}},
	})
	if !errors.Is(err, channels.ErrSendFailed) || !strings.Contains(err.Error(), "notes.md") {
		t.Fatalf("SendMedia = %v, want ErrSendFailed naming the file", err)
	}
	if len(api.calls) != 0 {
		t.Fatalf("Slack calls = %v, want none", api.calls)
	}
}
