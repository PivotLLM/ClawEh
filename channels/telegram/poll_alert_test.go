// ClawEh
// License: MIT

package telegram

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tenebris-tech/alerter"

	"github.com/PivotLLM/ClawEh/config"
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
func (r *alertRecorder) Normal(string, string, ...string)    {}
func (r *alertRecorder) Urgent(string, string, ...string)    {}
func (r *alertRecorder) Emergency(string, string, ...string) {}
func (r *alertRecorder) Close(context.Context) error         { return nil }

// TestPollFailureAlert drives telego's own logger, as wired by the factory, and
// checks a genuine fault alerts while a transient blip does not.
func TestPollFailureAlert(t *testing.T) {
	ch, err := NewTelegramChannelFromConfig(config.TelegramBotConfig{ID: "alice", Token: testToken}, nil)
	require.NoError(t, err)
	log := ch.bot.Logger()

	// Before an alerter is injected the hook must be a silent no-op.
	log.Errorf("Execution error getUpdates: request call: internal server error: 401")

	rec := &alertRecorder{}
	ch.SetAlerter(rec)

	log.Errorf("Execution error getUpdates: request call: internal server error: 502")
	log.Errorf("Retrying getting updates in 2s...")
	require.Empty(t, rec.alerts, "transient poll errors must not alert")

	log.Errorf("Execution error getUpdates: request call: internal server error: 401")
	require.Len(t, rec.alerts, 1)
	a := rec.alerts[0]
	require.False(t, a.Priority != alerter.Normal, "ClawEh alerts are normal priority")
	require.Equal(t, "Telegram polling failed", a.Title)
	require.Equal(t, "telegram-alice", a.EventID)
	require.Equal(t, "telegram-alice: Execution error getUpdates: request call: internal server error: 401", a.Description)
}

// TestAlertPollFailure_TrimsLongMessage keeps the description bounded.
func TestAlertPollFailure_TrimsLongMessage(t *testing.T) {
	ch := newTestChannel(t, &stubCaller{})
	rec := &alertRecorder{}
	ch.SetAlerter(rec)

	ch.alertPollFailure(strings.Repeat("x", 500))
	require.Len(t, rec.alerts, 1)
	require.Equal(t, "telegram: "+strings.Repeat("x", pollAlertMsgLimit)+"...", rec.alerts[0].Description)
}
