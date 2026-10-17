package slack

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/slack-go/slack"

	"github.com/PivotLLM/ClawEh/channels"
	"github.com/PivotLLM/ClawEh/config"
)

// Start honours its context: an auth.test Slack never answers ends when the
// context is cancelled, and Start returns the context's error.
func TestStart_HonoursContext(t *testing.T) {
	asked := make(chan struct{}, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		// Read the form first: only then does the server notice the client
		// going away and end the request's context.
		if _, err := io.Copy(io.Discard, r.Body); err != nil {
			t.Errorf("read request: %v", err)
		}
		if strings.HasSuffix(r.URL.Path, "/auth.test") {
			asked <- struct{}{}
		}
		<-r.Context().Done()
	}))
	t.Cleanup(srv.Close)
	c := &SlackChannel{
		BaseChannel: channels.NewBaseChannel("slack", config.SlackConfig{}, nil, []string{"*"}),
		api:         slack.New("xoxb-test", slack.OptionAPIURL(srv.URL+"/")),
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- c.Start(ctx) }()
	select {
	case <-asked:
	case <-time.After(5 * time.Second):
		t.Fatal("auth.test was not called")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Start = %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Start did not return once its context was cancelled")
	}
	if c.IsRunning() {
		t.Fatal("a cancelled start left the channel running")
	}
}
