package channels_test

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tenebris-tech/alerter"

	"github.com/PivotLLM/ClawEh/channels"
	"github.com/PivotLLM/ClawEh/channels/telegram"
	"github.com/PivotLLM/ClawEh/config"
	"github.com/PivotLLM/ClawEh/logger"
)

type recorder struct {
	mu     sync.Mutex
	alerts []alerter.Alert
}

func (r *recorder) Send(a alerter.Alert) {
	r.mu.Lock()
	r.alerts = append(r.alerts, a)
	r.mu.Unlock()
}
func (r *recorder) Normal(string, string, ...string)    {}
func (r *recorder) Urgent(string, string, ...string)    {}
func (r *recorder) Emergency(string, string, ...string) {}
func (r *recorder) Close(context.Context) error         { return nil }

// syncBuffer is a bytes.Buffer safe for the concurrent writes of several
// logging goroutines.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func (r *recorder) snapshot() []alerter.Alert {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]alerter.Alert(nil), r.alerts...)
}

// A Telegram bot whose API server drops every connection raises a
// "Telegram down" alert whose details, like the logs, carry no bot token.
func TestTelegramDownAlert_NoToken(t *testing.T) {
	const token = "1234567890:secretSECRETsecretSECRETsecretSECRE"
	secret := strings.SplitN(token, ":", 2)[1]

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
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
	defer srv.Close()

	var buf syncBuffer
	restore := logger.RedirectForTest(&buf)
	defer restore()

	ch, err := telegram.NewTelegramChannelFromConfig(config.TelegramBotConfig{
		ID: "redact", Token: token, BaseURL: srv.URL,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	rec := &recorder{}
	ch.SetAlerter(rec)
	ch.SetPlatform("telegram")
	channels.SetConnAlertAfter(ch.BaseChannel, 50*time.Millisecond)
	if err := ch.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	stop := func() {
		if err := ch.Stop(context.Background()); err != nil {
			t.Error(err)
		}
	}
	defer stop()

	var down *alerter.Alert
	for deadline := time.Now().Add(5 * time.Second); down == nil && time.Now().Before(deadline); {
		for _, a := range rec.snapshot() {
			if a.Title == "Telegram down" {
				down = &a
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	if down == nil {
		t.Fatalf("no Telegram down alert; alerts %+v", rec.snapshot())
	}
	if !strings.Contains(down.Details, "telegram-redact") || !strings.Contains(down.Details, "bot<redacted>") {
		t.Fatalf("details should name the bot and the redacted error: %q", down.Details)
	}
	if strings.Contains(down.Details, secret) {
		t.Fatalf("alert details carry the bot token: %q", down.Details)
	}
	stop() // flush the poll loop's last log lines before reading them
	if strings.Contains(buf.String(), secret) {
		t.Fatal("logs carry the bot token")
	}
}
