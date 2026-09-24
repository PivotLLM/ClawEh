// ClawEh
// License: MIT

package voice

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/tenebris-tech/alerter"
)

type alertRecorder struct {
	mu     sync.Mutex
	alerts []alerter.Alert
}

func (r *alertRecorder) Send(a alerter.Alert) {
	r.mu.Lock()
	r.alerts = append(r.alerts, a)
	r.mu.Unlock()
}
func (r *alertRecorder) High(string, string, ...string) {}
func (r *alertRecorder) Low(string, string, ...string)  {}
func (r *alertRecorder) Close(context.Context) error    { return nil }

// statusServer answers every request with the given status and body.
func statusServer(t *testing.T, status int, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, body, status)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// TestTranscribe_AlertsOnRejection: a 401/402/403 from the API raises one high
// alert keyed by the provider (credentials or credit, so every later request
// fails the same way); a 500 is a per-request failure and raises none. Both
// transcriber protocols behave the same way.
func TestTranscribe_AlertsOnRejection(t *testing.T) {
	audioPath := filepath.Join(t.TempDir(), "clip.ogg")
	if err := os.WriteFile(audioPath, []byte("fake-audio-data"), 0o644); err != nil {
		t.Fatal(err)
	}
	longBody := strings.Repeat("é", 300)

	build := map[string]func(base string, a alerter.Alerter) Transcriber{
		"groq": func(base string, a alerter.Alerter) Transcriber {
			tr := NewWhisperTranscriber("groq", "sk-bad", "", "", a)
			tr.apiBase = base
			return tr
		},
		"openrouter": func(base string, a alerter.Alerter) Transcriber {
			tr := NewOpenRouterTranscriber("sk-bad", "", "", a)
			tr.apiBase = base
			return tr
		},
	}

	for provider, newTranscriber := range build {
		t.Run(provider, func(t *testing.T) {
			rec := &alertRecorder{}
			for _, status := range []int{http.StatusInternalServerError, http.StatusTooManyRequests} {
				tr := newTranscriber(statusServer(t, status, "later").URL, rec)
				if _, err := tr.Transcribe(context.Background(), audioPath); err == nil {
					t.Fatalf("status %d must fail", status)
				}
			}
			if len(rec.alerts) != 0 {
				t.Fatalf("transient statuses must not alert, got %+v", rec.alerts)
			}

			for _, status := range []int{http.StatusUnauthorized, http.StatusPaymentRequired, http.StatusForbidden} {
				rec := &alertRecorder{}
				tr := newTranscriber(statusServer(t, status, longBody).URL, rec)
				if _, err := tr.Transcribe(context.Background(), audioPath); err == nil {
					t.Fatalf("status %d must fail", status)
				}
				if len(rec.alerts) != 1 {
					t.Fatalf("status %d must alert once, got %+v", status, rec.alerts)
				}
				a := rec.alerts[0]
				if !a.High || a.Title != "Voice transcription rejected" || a.EventID != "voice:"+provider {
					t.Fatalf("status %d: want high alert keyed by provider, got %+v", status, a)
				}
				if !strings.HasPrefix(a.Description, provider+": status ") {
					t.Fatalf("status %d: description must name the provider and status, got %q", status, a.Description)
				}
				if n := len([]rune(a.Details)); n != 200 {
					t.Fatalf("status %d: details must be trimmed to 200 runes, got %d", status, n)
				}
			}
		})
	}
}
