// ClawEh
// License: MIT

package telegram

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	ta "github.com/mymmrac/telego/telegoapi"
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

// TestPollFailureAlert_ChannelName keys the rejected-token alert on the bot's
// channel name, at normal priority, and is silent before an alerter is set.
func TestPollFailureAlert_ChannelName(t *testing.T) {
	ch, err := NewTelegramChannelFromConfig(config.TelegramBotConfig{ID: "alice", Token: testToken}, nil)
	require.NoError(t, err)
	unauthorized := &ta.Error{ErrorCode: 401, Description: "Unauthorized"}

	ch.pollFailed(unauthorized, time.Minute) // no alerter yet: a no-op

	rec := &alertRecorder{}
	ch.SetAlerter(rec)
	ch.pollFailed(unauthorized, time.Minute)
	require.Len(t, rec.alerts, 1)
	a := rec.alerts[0]
	require.Equal(t, alerter.Normal, a.Priority, "ClawEh alerts are normal priority")
	require.Equal(t, "telegram-alice", a.EventID)
	require.Equal(t, `telegram-alice: 401 "Unauthorized"`, a.Description)
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
