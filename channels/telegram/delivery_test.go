package telegram

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
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/PivotLLM/ClawEh/bus"
	"github.com/PivotLLM/ClawEh/channels"
	"github.com/PivotLLM/ClawEh/config"
	"github.com/PivotLLM/ClawEh/media"
)

const getMeReply = `{"ok":true,"result":{"id":1,"is_bot":true,"first_name":"t","username":"t_bot"}}`

// writeBody writes a fake Bot API reply.
func writeBody(t *testing.T, w io.Writer, body string) {
	t.Helper()
	if _, err := io.WriteString(w, body); err != nil {
		t.Errorf("write reply: %v", err)
	}
}

// newFakeBotChannel returns a running channel whose Bot API is srv.
func newFakeBotChannel(t *testing.T, srv *httptest.Server) *TelegramChannel {
	t.Helper()
	ch, err := NewTelegramChannelFromConfig(config.TelegramBotConfig{Token: testToken, BaseURL: srv.URL}, bus.NewMessageBus())
	require.NoError(t, err)
	ch.SetRunning(true)
	t.Cleanup(func() { ch.SetRunning(false) })
	return ch
}

// The plain-text retry after an HTML parse error sends the original text
// without a parse mode, to the same chat.
func TestSend_ParseErrorRetriesAsPlainText(t *testing.T) {
	var (
		mu       sync.Mutex
		requests []map[string]any
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/getMe") {
			writeBody(t, w, getMeReply)
			return
		}
		var req map[string]any
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode %s: %v", r.URL.Path, err)
		}
		mu.Lock()
		requests = append(requests, req)
		n := len(requests)
		mu.Unlock()
		if n == 1 {
			writeBody(t, w, `{"ok":false,"error_code":400,"description":"Bad Request: can't parse entities: unexpected end tag"}`)
			return
		}
		writeBody(t, w, `{"ok":true,"result":{"message_id":2,"date":0,"chat":{"id":12345,"type":"private"}}}`)
	}))
	t.Cleanup(srv.Close)

	ch := newFakeBotChannel(t, srv)
	require.NoError(t, ch.Send(context.Background(), bus.OutboundMessage{Channel: "telegram", ChatID: "12345", Content: "**hello**"}))

	mu.Lock()
	defer mu.Unlock()
	require.Len(t, requests, 2)
	require.Equal(t, "HTML", requests[0]["parse_mode"])
	require.NotContains(t, requests[1], "parse_mode")
	require.Equal(t, "**hello**", requests[1]["text"])
	require.Equal(t, requests[0]["chat_id"], requests[1]["chat_id"])
}

// A connection the Bot API drops is a temporary failure, retried by the
// manager, and its error carries no bot token.
func TestSend_NetworkErrorIsTemporary(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/getMe") {
			w.Header().Set("Content-Type", "application/json")
			writeBody(t, w, getMeReply)
			return
		}
		hj, ok := w.(http.Hijacker)
		if !ok {
			t.Error("response writer cannot be hijacked")
			return
		}
		conn, _, err := hj.Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		if err := conn.Close(); err != nil {
			t.Error(err)
		}
	}))
	t.Cleanup(srv.Close)

	ch := newFakeBotChannel(t, srv)
	err := ch.Send(context.Background(), bus.OutboundMessage{Channel: "telegram", ChatID: "12345", Content: "hello"})
	if !errors.Is(err, channels.ErrTemporary) {
		t.Fatalf("Send = %v, want ErrTemporary", err)
	}
	require.NotContains(t, err.Error(), strings.Split(testToken, ":")[1])
}

// SendMedia classifies Telegram's refusals as Send does.
func TestSendMedia_ClassifiesAPIErrors(t *testing.T) {
	tests := []struct {
		name     string
		body     string
		want     error
		wantText string
	}{
		{"chat not found", `{"ok":false,"error_code":400,"description":"Bad Request: chat not found"}`, channels.ErrRecipientNotFound, "chat not found"},
		{"blocked by the user", `{"ok":false,"error_code":403,"description":"Forbidden: bot was blocked by the user"}`, channels.ErrRecipientNotFound, "blocked by the user"},
		{"upgraded to a supergroup", `{"ok":false,"error_code":400,"description":"Bad Request: group chat was upgraded to a supergroup chat","parameters":{"migrate_to_chat_id":-1001234}}`, channels.ErrRecipientNotFound, "upgraded to a supergroup"},
		{"rate limited", `{"ok":false,"error_code":429,"description":"Too Many Requests: retry after 5","parameters":{"retry_after":5}}`, channels.ErrRateLimit, "retry after 5"},
		{"other bad request", `{"ok":false,"error_code":400,"description":"Bad Request: file is too big"}`, channels.ErrSendFailed, "file is too big"},
		{"server error", `{"ok":false,"error_code":500,"description":"Internal Server Error"}`, channels.ErrTemporary, "Internal Server Error"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var sends atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if strings.HasSuffix(r.URL.Path, "/getMe") {
					writeBody(t, w, getMeReply)
					return
				}
				if strings.HasSuffix(r.URL.Path, "/sendDocument") {
					sends.Add(1)
				}
				writeBody(t, w, tt.body)
			}))
			t.Cleanup(srv.Close)

			ch := newFakeBotChannel(t, srv)
			store := media.NewFileMediaStore()
			ch.SetMediaStore(store)
			path := filepath.Join(t.TempDir(), "report.txt")
			require.NoError(t, os.WriteFile(path, []byte("report"), 0o600))
			ref, err := store.Store(path, media.MediaMeta{Filename: "report.txt"}, "test")
			require.NoError(t, err)

			err = ch.SendMedia(context.Background(), bus.OutboundMediaMessage{
				Channel: "telegram", ChatID: "12345",
				Parts: []bus.MediaPart{{Type: "file", Ref: ref}},
			})
			if !errors.Is(err, tt.want) {
				t.Fatalf("SendMedia = %v, want %v", err, tt.want)
			}
			require.Equal(t, int32(1), sends.Load())
			require.Contains(t, err.Error(), tt.wantText)
			require.NotContains(t, err.Error(), strings.Split(testToken, ":")[1])
		})
	}
}
