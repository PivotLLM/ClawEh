package matrix

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/id"

	"github.com/PivotLLM/ClawEh/bus"
	"github.com/PivotLLM/ClawEh/channels"
	"github.com/PivotLLM/ClawEh/media"
)

// matrixMediaServer is a homeserver stub recording uploads and the media
// messages sent; uploadStatus/sendStatus, when set, fail those calls.
type matrixMediaServer struct {
	mu           sync.Mutex
	uploads      []string // "filename|content-type|body"
	events       []map[string]any
	uploadStatus int
	sendStatus   int
}

func (s *matrixMediaServer) handler(t *testing.T) http.Handler {
	t.Helper()
	reply := func(w io.Writer, body string) {
		if _, err := io.WriteString(w, body); err != nil {
			t.Errorf("write reply: %v", err)
		}
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		switch {
		case strings.Contains(r.URL.Path, "/media/") && strings.HasSuffix(r.URL.Path, "/upload"):
			if s.uploadStatus != 0 {
				w.WriteHeader(s.uploadStatus)
				reply(w, `{"errcode":"M_UNKNOWN","error":"upload refused"}`)
				return
			}
			body, err := io.ReadAll(r.Body)
			if err != nil {
				t.Errorf("read upload: %v", err)
			}
			s.uploads = append(s.uploads, r.URL.Query().Get("filename")+"|"+r.Header.Get("Content-Type")+"|"+string(body))
			reply(w, `{"content_uri":"mxc://matrix.test/up`+string(rune('0'+len(s.uploads)))+`"}`)
		case strings.Contains(r.URL.Path, "/send/m.room.message/"):
			if s.sendStatus != 0 {
				w.WriteHeader(s.sendStatus)
				reply(w, `{"errcode":"M_FORBIDDEN","error":"no"}`)
				return
			}
			var ev map[string]any
			if err := json.NewDecoder(r.Body).Decode(&ev); err != nil {
				t.Errorf("decode event: %v", err)
			}
			s.events = append(s.events, ev)
			reply(w, `{"event_id":"$e"}`)
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	})
}

func newMediaTestChannel(t *testing.T, srv *matrixMediaServer) (*MatrixChannel, *media.FileMediaStore) {
	t.Helper()
	hs := httptest.NewServer(srv.handler(t))
	t.Cleanup(hs.Close)
	client, err := mautrix.NewClient(hs.URL, id.UserID("@claw:matrix.test"), "tok")
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	c := &MatrixChannel{BaseChannel: channels.NewBaseChannel("matrix", nil, nil, nil), client: client}
	store := media.NewFileMediaStore()
	c.SetMediaStore(store)
	c.SetRunning(true)
	return c, store
}

// storeFile writes body to a temp file and stores it, returning its ref.
func storeFile(t *testing.T, store *media.FileMediaStore, name, body, contentType string) (string, string) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "staged-"+name)
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	ref, err := store.Store(p, media.MediaMeta{Filename: name, ContentType: contentType}, "test")
	if err != nil {
		t.Fatal(err)
	}
	return ref, p
}

// Every part is uploaded under the part's own file name and posted, in order.
func TestSendMedia_UploadsEveryPartInOrder(t *testing.T) {
	srv := &matrixMediaServer{}
	c, store := newMediaTestChannel(t, srv)
	ref1, _ := storeFile(t, store, "a.png", "PNGDATA", "image/png")
	ref2, _ := storeFile(t, store, "b.txt", "hello", "")

	err := c.SendMedia(context.Background(), bus.OutboundMediaMessage{
		Channel: "matrix", ChatID: "!room:matrix.test",
		Parts: []bus.MediaPart{
			{Type: "image", Ref: ref1, Filename: "shown.png"},
			{Type: "file", Ref: ref2},
		},
	})
	if err != nil {
		t.Fatalf("SendMedia = %v", err)
	}
	if len(srv.uploads) != 2 || len(srv.events) != 2 {
		t.Fatalf("uploads=%v events=%d, want 2 each", srv.uploads, len(srv.events))
	}
	if srv.uploads[0] != "shown.png|image/png|PNGDATA" {
		t.Errorf("first upload = %q", srv.uploads[0])
	}
	if !strings.HasPrefix(srv.uploads[1], "b.txt|text/plain") || !strings.HasSuffix(srv.uploads[1], "|hello") {
		t.Errorf("second upload = %q, want the stored name and a type from its extension", srv.uploads[1])
	}
	if srv.events[0]["msgtype"] != "m.image" || srv.events[1]["msgtype"] != "m.file" {
		t.Errorf("msgtypes = %v, %v", srv.events[0]["msgtype"], srv.events[1]["msgtype"])
	}
}

// A part that can no longer be read fails the send, naming the file, and
// nothing after it is sent.
func TestSendMedia_UnreadablePartFailsTheSend(t *testing.T) {
	cases := []struct {
		name   string
		break_ func(path string) error
	}{
		{"deleted", os.Remove},
		{"unreadable", func(p string) error { return os.Chmod(p, 0) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.name == "unreadable" && os.Geteuid() == 0 {
				t.Skip("root reads any file")
			}
			srv := &matrixMediaServer{}
			c, store := newMediaTestChannel(t, srv)
			ref1, p1 := storeFile(t, store, "gone.txt", "x", "text/plain")
			ref2, _ := storeFile(t, store, "next.txt", "y", "text/plain")
			if err := tc.break_(p1); err != nil {
				t.Fatal(err)
			}

			err := c.SendMedia(context.Background(), bus.OutboundMediaMessage{
				ChatID: "!room:matrix.test",
				Parts:  []bus.MediaPart{{Type: "file", Ref: ref1, Filename: "gone.txt"}, {Type: "file", Ref: ref2}},
			})
			if !errors.Is(err, channels.ErrSendFailed) || !strings.Contains(err.Error(), "gone.txt") {
				t.Fatalf("SendMedia = %v, want ErrSendFailed naming gone.txt", err)
			}
			if len(srv.uploads) != 0 || len(srv.events) != 0 {
				t.Fatalf("sent after a failed part: uploads=%v events=%d", srv.uploads, len(srv.events))
			}
		})
	}
}

// Upload and post failures are temporary (the manager retries them).
func TestSendMedia_ServerFailuresAreTemporary(t *testing.T) {
	for _, tc := range []struct {
		name string
		srv  *matrixMediaServer
	}{
		{"upload", &matrixMediaServer{uploadStatus: http.StatusInternalServerError}},
		{"post", &matrixMediaServer{sendStatus: http.StatusForbidden}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, store := newMediaTestChannel(t, tc.srv)
			ref, _ := storeFile(t, store, "a.txt", "x", "text/plain")
			err := c.SendMedia(context.Background(), bus.OutboundMediaMessage{
				ChatID: "!room:matrix.test", Parts: []bus.MediaPart{{Type: "file", Ref: ref}},
			})
			if !errors.Is(err, channels.ErrTemporary) {
				t.Fatalf("SendMedia = %v, want ErrTemporary", err)
			}
		})
	}
}

// The refusals before any part: not running, no room, no store, cancelled.
func TestSendMedia_Preconditions(t *testing.T) {
	srv := &matrixMediaServer{}
	c, store := newMediaTestChannel(t, srv)
	ref, _ := storeFile(t, store, "a.txt", "x", "text/plain")
	msg := bus.OutboundMediaMessage{ChatID: "!room:matrix.test", Parts: []bus.MediaPart{{Type: "file", Ref: ref}}}

	c.SetRunning(false)
	if err := c.SendMedia(context.Background(), msg); !errors.Is(err, channels.ErrNotRunning) {
		t.Errorf("stopped: %v, want ErrNotRunning", err)
	}
	c.SetRunning(true)

	if err := c.SendMedia(context.Background(), bus.OutboundMediaMessage{ChatID: "  ", Parts: msg.Parts}); !errors.Is(err, channels.ErrSendFailed) {
		t.Errorf("no room: %v, want ErrSendFailed", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := c.SendMedia(ctx, msg); !errors.Is(err, context.Canceled) {
		t.Errorf("cancelled: %v, want context.Canceled", err)
	}

	c.SetMediaStore(nil)
	if err := c.SendMedia(context.Background(), msg); !errors.Is(err, channels.ErrSendFailed) {
		t.Errorf("no store: %v, want ErrSendFailed", err)
	}
	if len(srv.uploads) != 0 {
		t.Errorf("uploads = %v, want none", srv.uploads)
	}
}
