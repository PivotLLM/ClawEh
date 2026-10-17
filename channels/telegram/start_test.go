package telegram

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/PivotLLM/ClawEh/bus"
	"github.com/PivotLLM/ClawEh/config"
)

// Start does not wait for getMe: with the API never answering it, Start
// still returns, and Stop does too.
func TestStart_DoesNotWaitForGetMe(t *testing.T) {
	getMe := make(chan struct{}, 1)
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/getMe"):
			select {
			case getMe <- struct{}{}:
			default:
			}
			<-release // never answered while the test runs
		case strings.HasSuffix(r.URL.Path, "/getUpdates"):
			select {
			case <-r.Context().Done():
			case <-release:
			}
		default:
			_, _ = w.Write([]byte(`{"ok":true,"result":true}`))
		}
	}))
	t.Cleanup(func() {
		close(release)
		srv.Close()
	})

	ch, err := NewTelegramChannelFromConfig(config.TelegramBotConfig{Token: testToken, BaseURL: srv.URL}, bus.NewMessageBus())
	if err != nil {
		t.Fatal(err)
	}
	started := make(chan error, 1)
	go func() { started <- ch.Start(context.Background()) }()
	select {
	case err := <-started:
		if err != nil {
			t.Fatalf("Start = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Start waited for getMe")
	}
	select {
	case <-getMe:
	case <-time.After(5 * time.Second):
		t.Fatal("getMe was never asked")
	}
	if err := ch.Stop(context.Background()); err != nil {
		t.Fatalf("Stop = %v", err)
	}
}
