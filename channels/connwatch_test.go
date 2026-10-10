package channels

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/tenebris-tech/alerter"

	"github.com/PivotLLM/ClawEh/internal/clock"
)

// The connection watch tests run on a fake clock: an outage's alert is due
// exactly ConnDownAlertAfter after its first failure.

func newWatchedChannel(t *testing.T) (*BaseChannel, *alertRecorder, *clock.Fake) {
	t.Helper()
	rec := &alertRecorder{}
	fc := clock.NewFake(time.Now())
	return newPlatformChannel(t, "test", newConnAggregator(), rec, fc), rec, fc
}

// newPlatformChannel returns a running channel named name that reports its
// outages to g and its alerts to rec, on the clock fc.
func newPlatformChannel(t *testing.T, name string, g *connAggregator, rec *alertRecorder, fc *clock.Fake) *BaseChannel {
	t.Helper()
	bc := NewBaseChannel(name, nil, nil, nil)
	bc.outages = g
	bc.SetAlerter(rec)
	bc.conn.clock = fc
	bc.SetRunning(true)
	return bc
}

func snapshot(rec *alertRecorder) []alerter.Alert {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	return append([]alerter.Alert(nil), rec.alerts...)
}

func recorded(rec *alertRecorder) int {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	return len(rec.alerts)
}

// waitPastAlert moves fc to the end of the alert window of an outage that
// began now; the alert, if any, is raised before it returns.
func waitPastAlert(fc *clock.Fake) { fc.Advance(ConnDownAlertAfter) }

// A failure run that outlasts the window raises exactly one alert carrying
// the latest error, however many failures follow.
func TestConnWatch_ProlongedOutageAlertsOnce(t *testing.T) {
	bc, rec, fc := newWatchedChannel(t)
	bc.ReportConnFailure(errors.New("first"))
	bc.ReportConnFailure(errors.New("latest"))
	fc.Advance(ConnDownAlertAfter - time.Nanosecond)
	if n := recorded(rec); n != 0 {
		t.Fatalf("alerted before the window ended: %d alerts", n)
	}
	fc.Advance(time.Nanosecond)
	if n := recorded(rec); n != 1 {
		t.Fatalf("no alert when the window ended: %d alerts", n)
	}
	bc.ReportConnFailure(errors.New("after alert"))
	waitPastAlert(fc)

	if n := recorded(rec); n != 1 {
		t.Fatalf("want 1 alert, got %d", n)
	}
	a := rec.alerts[0]
	if a.Title != "Test down" || a.EventID != "test" || a.Details != "Channels: test\nLast error: latest" {
		t.Fatalf("unexpected alert %+v", a)
	}
}

// Recovery inside the window cancels the alert.
func TestConnWatch_RecoveryBeforeWindowDoesNotAlert(t *testing.T) {
	bc, rec, fc := newWatchedChannel(t)
	bc.ReportConnFailure(errors.New("429"))
	bc.ReportConnected()
	waitPastAlert(fc)
	if n := recorded(rec); n != 0 {
		t.Fatalf("recovered outage must not alert, got %d", n)
	}
}

// A connection that never fails never alerts.
func TestConnWatch_HealthyNeverAlerts(t *testing.T) {
	bc, rec, fc := newWatchedChannel(t)
	bc.ReportConnected()
	waitPastAlert(fc)
	if n := recorded(rec); n != 0 {
		t.Fatalf("healthy channel must not alert, got %d", n)
	}
}

// After recovery, a new outage gets its own alert.
func TestConnWatch_NewOutageAfterRecoveryAlertsAgain(t *testing.T) {
	bc, rec, fc := newWatchedChannel(t)
	bc.ReportConnFailure(errors.New("one"))
	waitPastAlert(fc)
	bc.ReportConnected()
	bc.ReportConnFailure(errors.New("two"))
	waitPastAlert(fc)
	alerts := snapshot(rec)
	if len(alerts) != 3 || alerts[0].Title != "Test down" || alerts[1].Title != "Test up" || alerts[2].Title != "Test down" {
		t.Fatalf("want down, up, down; got %+v", alerts)
	}
}

// A channel stopped mid-outage is not retrying, so it does not alert, and a
// later outage starts a fresh clock.
func TestConnWatch_StoppedChannelDoesNotAlert(t *testing.T) {
	bc, rec, fc := newWatchedChannel(t)
	bc.ReportConnFailure(errors.New("down"))
	bc.SetRunning(false)
	waitPastAlert(fc)
	if n := recorded(rec); n != 0 {
		t.Fatalf("stopped channel must not alert, got %d", n)
	}

	bc.SetRunning(true)
	bc.ReportConnFailure(errors.New("down again"))
	waitPastAlert(fc)
	if n := recorded(rec); n != 1 {
		t.Fatalf("restarted channel's new outage must alert, got %d", n)
	}
}

// Seven bots of one platform failing together raise one down alert, and one
// up alert once the last of them reconnects.
func TestConnWatch_PlatformOutageAlertsOnce(t *testing.T) {
	g, rec, fc := newConnAggregator(), &alertRecorder{}, clock.NewFake(time.Now())
	bots := make([]*BaseChannel, 0, 7)
	for i := range 7 {
		bots = append(bots, newPlatformChannel(t, fmt.Sprintf("telegram-bot%d", i), g, rec, fc))
	}
	for _, b := range bots {
		b.ReportConnFailure(errors.New("dial tcp: i/o timeout"))
	}
	waitPastAlert(fc)
	for _, b := range bots {
		b.ReportConnFailure(errors.New("still down"))
	}

	alerts := snapshot(rec)
	if len(alerts) != 1 {
		t.Fatalf("want 1 down alert, got %+v", alerts)
	}
	down := alerts[0]
	if down.Title != "Telegram down" || down.EventID != "telegram" ||
		!strings.HasPrefix(down.Description, "7 bots, no working connection for ") ||
		!strings.HasPrefix(down.Details, "Channels: telegram-bot0, telegram-bot1, telegram-bot2, telegram-bot3, telegram-bot4, telegram-bot5, telegram-bot6\nLast error: ") {
		t.Fatalf("unexpected down alert %+v", down)
	}

	for _, b := range bots[:6] {
		b.ReportConnected()
	}
	if n := recorded(rec); n != 1 {
		t.Fatalf("no up alert while a bot is still down, got %d alerts", n)
	}
	bots[6].ReportConnected()
	alerts = snapshot(rec)
	if len(alerts) != 2 {
		t.Fatalf("want down then up, got %+v", alerts)
	}
	up := alerts[1]
	if up.Title != "Telegram up" || up.EventID != "telegram-up" || !strings.HasPrefix(up.Description, "back after ") {
		t.Fatalf("unexpected up alert %+v", up)
	}
}

// Outages on two platforms raise one alert each.
func TestConnWatch_TwoPlatformsAlertSeparately(t *testing.T) {
	g, rec, fc := newConnAggregator(), &alertRecorder{}, clock.NewFake(time.Now())
	tg1 := newPlatformChannel(t, "telegram-a", g, rec, fc)
	tg2 := newPlatformChannel(t, "telegram-b", g, rec, fc)
	sl := newPlatformChannel(t, "slack", g, rec, fc)
	for _, c := range []*BaseChannel{tg1, tg2, sl} {
		c.ReportConnFailure(errors.New("down"))
	}
	waitPastAlert(fc)

	alerts := snapshot(rec)
	if len(alerts) != 2 {
		t.Fatalf("want one alert per platform (2), got %+v", alerts)
	}
	byID := map[string]alerter.Alert{}
	for _, a := range alerts {
		byID[a.EventID] = a
	}
	if byID["telegram"].Title != "Telegram down" || !strings.HasPrefix(byID["telegram"].Description, "2 bots,") {
		t.Fatalf("unexpected telegram alert %+v", byID["telegram"])
	}
	if byID["slack"].Title != "Slack down" || !strings.HasPrefix(byID["slack"].Description, "1 bot,") {
		t.Fatalf("unexpected slack alert %+v", byID["slack"])
	}
}

// A channel that stops mid-outage leaves the platform's outage: the others
// reconnecting is enough for the up alert.
func TestConnWatch_StoppedChannelLeavesPlatformOutage(t *testing.T) {
	g, rec, fc := newConnAggregator(), &alertRecorder{}, clock.NewFake(time.Now())
	a := newPlatformChannel(t, "telegram-a", g, rec, fc)
	b := newPlatformChannel(t, "telegram-b", g, rec, fc)
	a.ReportConnFailure(errors.New("down"))
	b.ReportConnFailure(errors.New("down"))
	waitPastAlert(fc)
	if n := recorded(rec); n != 1 {
		t.Fatalf("want 1 down alert, got %d", n)
	}

	b.SetRunning(false)
	if n := recorded(rec); n != 1 {
		t.Fatalf("stopping a channel must not alert, got %d", n)
	}
	a.ReportConnected()
	alerts := snapshot(rec)
	if len(alerts) != 2 || alerts[1].Title != "Telegram up" {
		t.Fatalf("want up alert once the remaining bot reconnects, got %+v", alerts)
	}
}

// An outage whose channels all stop ends without an up alert, and the next
// outage alerts afresh.
func TestConnWatch_AllStoppedEndsOutageSilently(t *testing.T) {
	g, rec, fc := newConnAggregator(), &alertRecorder{}, clock.NewFake(time.Now())
	a := newPlatformChannel(t, "telegram-a", g, rec, fc)
	a.ReportConnFailure(errors.New("down"))
	waitPastAlert(fc)
	a.SetRunning(false)
	if n := recorded(rec); n != 1 {
		t.Fatalf("want only the down alert, got %d", n)
	}

	a.SetRunning(true)
	a.ReportConnFailure(errors.New("down again"))
	waitPastAlert(fc)
	alerts := snapshot(rec)
	if len(alerts) != 2 || alerts[1].Title != "Telegram down" {
		t.Fatalf("want a fresh down alert, got %+v", alerts)
	}
}

// The alert text at the production threshold.
func TestConnAlertText(t *testing.T) {
	g := newConnAggregator()
	tg := NewBaseChannel("telegram-Alice", nil, nil, nil)
	g.failure(tg, time.Now().Add(-18*time.Minute), "boom")
	down := g.expired(tg, ConnDownAlertAfter)
	want := alerter.Alert{
		Title:       "Telegram down",
		Description: "1 bot, no working connection for 10m",
		Details:     "Channels: telegram-Alice\nLast error: boom",
		EventID:     "telegram",
	}
	if *down != want {
		t.Fatalf("down alert = %+v, want %+v", *down, want)
	}
	up := g.connected(tg)
	if up == nil || *up != (alerter.Alert{Title: "Telegram up", Description: "back after 18m", EventID: "telegram-up"}) {
		t.Fatalf("unexpected up alert %+v", up)
	}

	mx := NewBaseChannel("matrix", nil, nil, nil)
	mx.SetPlatform("matrix")
	g.failure(mx, time.Now(), "sync failed")
	if d := g.expired(mx, ConnDownAlertAfter).Description; d != "1 connection, no working connection for 10m" {
		t.Fatalf("matrix description = %q", d)
	}

	for d, want := range map[time.Duration]string{
		45 * time.Second:                "45s",
		10 * time.Minute:                "10m",
		18*time.Minute + 20*time.Second: "18m",
		time.Hour:                       "1h",
		time.Hour + 5*time.Minute:       "1h5m",
	} {
		if got := shortDuration(d); got != want {
			t.Fatalf("shortDuration(%s) = %q, want %q", d, got, want)
		}
	}
	if got := countNoun("telegram", 7); got != "7 bots" {
		t.Fatalf("countNoun = %q", got)
	}
}

func TestNextConnRetry(t *testing.T) {
	if got := NextConnRetry(0); got != ConnRetryMin {
		t.Fatalf("first wait = %s, want %s", got, ConnRetryMin)
	}
	if got := NextConnRetry(ConnRetryMin); got != 2*ConnRetryMin {
		t.Fatalf("second wait = %s, want %s", got, 2*ConnRetryMin)
	}
	if got := NextConnRetry(ConnRetryMax); got != ConnRetryMax {
		t.Fatalf("capped wait = %s, want %s", got, ConnRetryMax)
	}
}
