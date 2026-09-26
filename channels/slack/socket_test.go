package slack

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/slack-go/slack"
	"github.com/slack-go/slack/socketmode"

	"github.com/PivotLLM/ClawEh/channels"
	"github.com/PivotLLM/ClawEh/internal/testalerts"
)

// startAgainst runs the channel's Socket Mode loop against a fake Slack API
// whose apps.connections.open is served by open.
func startAgainst(t *testing.T, open http.HandlerFunc) (*SlackChannel, *testalerts.Recorder) {
	t.Helper()
	srv := httptest.NewServer(open)
	t.Cleanup(srv.Close)

	api := slack.New("xoxb-test", slack.OptionAppLevelToken("xapp-test"), slack.OptionAPIURL(srv.URL+"/"))
	c := &SlackChannel{
		BaseChannel:  channels.NewBaseChannel("slack", nil, nil, nil),
		api:          api,
		socketClient: socketmode.New(api),
	}
	rec := &testalerts.Recorder{}
	c.SetAlerter(rec)
	c.SetRunning(true)
	c.ctx, c.cancel = context.WithCancel(context.Background())
	t.Cleanup(c.cancel)

	go c.eventLoop()
	go c.runSocket()
	return c, rec
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// A rejected token alerts at once; the channel keeps its loop running.
func TestRunSocket_RejectedTokenAlerts(t *testing.T) {
	_, rec := startAgainst(t, func(w http.ResponseWriter, _ *http.Request) {
		if _, err := io.WriteString(w, `{"ok":false,"error":"invalid_auth"}`); err != nil {
			t.Errorf("write response: %v", err)
		}
	})
	waitFor(t, "credentials alert", func() bool { return len(rec.Alerts()) > 0 })
	a := rec.Alerts()[0]
	if a.Title != "Channel credentials rejected" || a.EventID != "slack" {
		t.Fatalf("unexpected alert %+v", a)
	}
}

// A server error is retried and tracked as an outage, without alerting.
func TestRunSocket_ServerErrorIsTrackedNotAlerted(t *testing.T) {
	c, rec := startAgainst(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	})
	waitFor(t, "outage report", func() bool { return !c.ConnDownSince().IsZero() })
	if n := len(rec.Alerts()); n != 0 {
		t.Fatalf("transient failure must not alert, got %d", n)
	}
}

func TestIsSlackAuthError(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want bool
	}{
		{errors.New("invalid_auth"), true},
		{errors.New("token_revoked"), true},
		{errors.New("account_inactive"), true},
		{errors.New("not_authed"), true},
		{fmt.Errorf("open: %w", slack.StatusCodeError{Code: http.StatusNotFound}), true},
		{slack.StatusCodeError{Code: http.StatusBadGateway}, false},
		{errors.New("ping timeout"), false},
	} {
		if got := isSlackAuthError(tc.err); got != tc.want {
			t.Errorf("isSlackAuthError(%v) = %v, want %v", tc.err, got, tc.want)
		}
	}
}
