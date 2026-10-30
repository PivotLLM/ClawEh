package channels

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/PivotLLM/ClawEh/bus"
	"github.com/PivotLLM/ClawEh/internal/clock"
	"github.com/PivotLLM/ClawEh/logger"
)

// checkedChannel is a channel whose connection check returns result, or,
// when block is set, waits for ctx to end (or, with ignoreCtx, for release).
type checkedChannel struct {
	*BaseChannel
	result    ConnCheckResult
	block     bool
	ignoreCtx bool
	release   chan struct{}
	calls     atomic.Int32
	onCheck   func() // called by ConnCheck before it returns, when set
}

func (c *checkedChannel) Start(context.Context) error                     { return nil }
func (c *checkedChannel) Stop(context.Context) error                      { return nil }
func (c *checkedChannel) Send(context.Context, bus.OutboundMessage) error { return nil }

func (c *checkedChannel) ConnCheck(ctx context.Context) ConnCheckResult {
	c.calls.Add(1)
	if c.onCheck != nil {
		c.onCheck()
	}
	switch {
	case c.ignoreCtx:
		<-c.release
	case c.block:
		<-ctx.Done()
		return ConnCheckResult{Reason: ConnCheckNoResponse}
	}
	return c.result
}

// newCheckedChannel returns a running channel named name, with result as its
// connection check, on g, rec and fc.
func newCheckedChannel(t *testing.T, name string, result ConnCheckResult, g *connAggregator, rec *alertRecorder, fc *clock.Fake) *checkedChannel {
	t.Helper()
	ch := &checkedChannel{BaseChannel: newPlatformChannel(t, name, g, rec, fc), result: result}
	ch.SetOwner(ch)
	return ch
}

// setConnCheckTimeout shortens connCheckTimeout for one test.
func setConnCheckTimeout(t *testing.T, d time.Duration) {
	t.Helper()
	old := connCheckTimeout
	connCheckTimeout = d
	t.Cleanup(func() { connCheckTimeout = old })
}

func TestConnCheck_DownAlertDetails(t *testing.T) {
	tests := []struct {
		name    string
		results map[string]ConnCheckResult
		want    string
	}{
		{
			name:    "one bot working",
			results: map[string]ConnCheckResult{"telegram-alice": {Working: true, Waiting: 3, WaitingKnown: true}},
			want:    "Channels: telegram-alice\nLast error: boom\nBot check: working\nWaiting messages: 3",
		},
		{
			name:    "one bot failing",
			results: map[string]ConnCheckResult{"telegram-alice": {Reason: "502", Waiting: 0, WaitingKnown: true}},
			want:    "Channels: telegram-alice\nLast error: boom\nBot check: failing (502)\nWaiting messages: 0",
		},
		{
			name:    "one bot, waiting unknown",
			results: map[string]ConnCheckResult{"telegram-alice": {Reason: ConnCheckNoResponse}},
			want:    "Channels: telegram-alice\nLast error: boom\nBot check: failing (no response)",
		},
		{
			name: "several bots",
			results: map[string]ConnCheckResult{
				"telegram-bob":   {Reason: "502"},
				"telegram-alice": {Working: true, Waiting: 0, WaitingKnown: true},
			},
			want: "Channels: telegram-alice, telegram-bob\nLast error: boom\n" +
				"Bot check telegram-alice: working, 0 waiting\n" +
				"Bot check telegram-bob: failing (502)",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g, rec, fc := newConnAggregator(), &alertRecorder{}, clock.NewFake(time.Now())
			for name, r := range tt.results {
				newCheckedChannel(t, name, r, g, rec, fc).ReportConnFailure(errors.New("boom"))
			}
			waitPastAlert(fc)
			alerts := snapshot(rec)
			if len(alerts) != 1 {
				t.Fatalf("want 1 alert, got %+v", alerts)
			}
			if alerts[0].Details != tt.want {
				t.Fatalf("details = %q, want %q", alerts[0].Details, tt.want)
			}
		})
	}
}

// Each check is logged with the channel, its result and the waiting count.
func TestConnCheck_Logged(t *testing.T) {
	var buf strings.Builder
	restore := logger.RedirectForTest(&buf)
	defer restore()

	g, rec, fc := newConnAggregator(), &alertRecorder{}, clock.NewFake(time.Now())
	newCheckedChannel(t, "telegram-alice", ConnCheckResult{Reason: "502", Waiting: 7, WaitingKnown: true}, g, rec, fc).
		ReportConnFailure(errors.New("boom"))
	waitPastAlert(fc)

	out := buf.String()
	for _, want := range []string{"Connection check", "telegram-alice", "failing (502)", `"waiting":7`} {
		if !strings.Contains(out, want) {
			t.Fatalf("log lacks %q:\n%s", want, out)
		}
	}
}

// A check that outlasts the timeout reports "no response", whether it
// honours its context or not, and the alert is still sent.
func TestConnCheck_Timeout(t *testing.T) {
	setConnCheckTimeout(t, 50*time.Millisecond)
	for _, ignoreCtx := range []bool{false, true} {
		g, rec, fc := newConnAggregator(), &alertRecorder{}, clock.NewFake(time.Now())
		ch := newCheckedChannel(t, "telegram-alice", ConnCheckResult{Working: true}, g, rec, fc)
		ch.block, ch.ignoreCtx, ch.release = true, ignoreCtx, make(chan struct{})
		defer close(ch.release)
		ch.ReportConnFailure(errors.New("boom"))
		waitPastAlert(fc)
		alerts := snapshot(rec)
		if len(alerts) != 1 || alerts[0].Details != "Channels: telegram-alice\nLast error: boom\nBot check: failing (no response)" {
			t.Fatalf("ignoreCtx=%v: unexpected alerts %+v", ignoreCtx, alerts)
		}
	}
}

// An alert naming several bots names the bot on each check line, even when
// only one of them has a check.
func TestConnCheck_SeveralBotsOneChecked(t *testing.T) {
	g, rec, fc := newConnAggregator(), &alertRecorder{}, clock.NewFake(time.Now())
	newCheckedChannel(t, "telegram-alice", ConnCheckResult{Working: true}, g, rec, fc).
		ReportConnFailure(errors.New("boom"))
	bc := newPlatformChannel(t, "telegram-bob", g, rec, fc)
	bc.SetOwner(&nonCheckedChannel{bc})
	bc.ReportConnFailure(errors.New("boom"))
	waitPastAlert(fc)
	alerts := snapshot(rec)
	want := "Channels: telegram-alice, telegram-bob\nLast error: boom\nBot check telegram-alice: working"
	if len(alerts) != 1 || alerts[0].Details != want {
		t.Fatalf("unexpected alerts %+v, want details %q", alerts, want)
	}
}

// A channel without a connection check gets today's alert, unchanged.
func TestConnCheck_PlatformWithoutCheckUnchanged(t *testing.T) {
	g, rec, fc := newConnAggregator(), &alertRecorder{}, clock.NewFake(time.Now())
	bc := newPlatformChannel(t, "slack-alice", g, rec, fc)
	bc.SetOwner(&nonCheckedChannel{bc})
	bc.ReportConnFailure(errors.New("boom"))
	waitPastAlert(fc)
	alerts := snapshot(rec)
	if len(alerts) != 1 || alerts[0].Details != "Channels: slack-alice\nLast error: boom" {
		t.Fatalf("unexpected alerts %+v", alerts)
	}
}

type nonCheckedChannel struct{ *BaseChannel }

func (c *nonCheckedChannel) Start(context.Context) error                     { return nil }
func (c *nonCheckedChannel) Stop(context.Context) error                      { return nil }
func (c *nonCheckedChannel) Send(context.Context, bus.OutboundMessage) error { return nil }

// A bot that joins an outage already alerted is not checked, and neither is
// the up alert.
func TestConnCheck_OnlyOnTheDownAlert(t *testing.T) {
	g, rec, fc := newConnAggregator(), &alertRecorder{}, clock.NewFake(time.Now())
	a := newCheckedChannel(t, "telegram-alice", ConnCheckResult{Working: true}, g, rec, fc)
	b := newCheckedChannel(t, "telegram-bob", ConnCheckResult{Working: true}, g, rec, fc)

	a.ReportConnFailure(errors.New("boom"))
	waitPastAlert(fc)
	b.ReportConnFailure(errors.New("boom"))
	waitPastAlert(fc)
	a.ReportConnected()
	b.ReportConnected()

	if n := a.calls.Load(); n != 1 {
		t.Fatalf("alice checked %d times, want 1", n)
	}
	if n := b.calls.Load(); n != 0 {
		t.Fatalf("bob joined the alerted outage but was checked %d times", n)
	}
	alerts := snapshot(rec)
	if len(alerts) != 2 || alerts[1].Title != "Telegram up: telegram-alice, telegram-bob" || alerts[1].Details != "" {
		t.Fatalf("want down then an unchanged up alert, got %+v", alerts)
	}
}

// A bot that recovers while its down alert is being prepared has its up
// alert sent right after the down alert, never before it.
func TestConnCheck_UpHeldUntilDownSent(t *testing.T) {
	g, rec, fc := newConnAggregator(), &alertRecorder{}, clock.NewFake(time.Now())
	ch := newCheckedChannel(t, "telegram-alice", ConnCheckResult{Working: true}, g, rec, fc)
	// The bot recovers while its down alert is being prepared.
	ch.onCheck = func() { ch.ReportConnected() }

	ch.ReportConnFailure(errors.New("boom"))
	waitPastAlert(fc)

	alerts := snapshot(rec)
	if len(alerts) != 2 || alerts[0].Title != "Telegram down: telegram-alice" || alerts[1].Title != "Telegram up: telegram-alice" {
		t.Fatalf("want down then up, got %+v", alerts)
	}

	// The next outage alerts afresh, with nothing left held.
	ch.onCheck = nil
	ch.ReportConnFailure(errors.New("again"))
	waitPastAlert(fc)
	ch.ReportConnected()
	if alerts := snapshot(rec); len(alerts) != 4 || alerts[2].Title != "Telegram down: telegram-alice" || alerts[3].Title != "Telegram up: telegram-alice" {
		t.Fatalf("want a second down then up, got %+v", alerts)
	}
}
