// ClawEh
// License: MIT

package channels

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/tenebris-tech/alerter"
	"golang.org/x/time/rate"

	"github.com/PivotLLM/ClawEh/alerts"
	"github.com/PivotLLM/ClawEh/bus"
	"github.com/PivotLLM/ClawEh/internal/clock"
)

// rejected is the alert a channel raises on every retry of a rejected token.
func rejected() alerter.Alert {
	return alerter.Alert{Title: "Channel credentials rejected", Description: "test: token rejected"}
}

// TestAlertLasting_OnceThenDaily: a fault met on every retry alerts once,
// keyed by the channel, then once a day with a note of how long it has lasted
// and how many reports were held.
func TestAlertLasting_OnceThenDaily(t *testing.T) {
	bc, rec, fc := newWatchedChannel(t)
	start := fc.Now()

	for range 60 { // an hour of retries, one a minute
		bc.AlertLasting(rejected())
		fc.Advance(time.Minute)
	}
	got := snapshot(rec)
	if len(got) != 1 || got[0].EventID != "test" || got[0].Description != "test: token rejected" {
		t.Fatalf("an hour of retries must raise one alert for the channel, got %+v", got)
	}

	fc.Advance(alerts.RemindEvery - time.Hour)
	bc.AlertLasting(rejected())
	got = snapshot(rec)
	if len(got) != 2 {
		t.Fatalf("a day later the fault must alert once more, got %+v", got)
	}
	want := "test: token rejected; ongoing since " + alerts.Stamp(start) + ", 59 more since the last alert"
	if got[1].Description != want {
		t.Fatalf("reminder = %q, want %q", got[1].Description, want)
	}
}

// TestAlertLasting_EndsWhenConnected: a working connection ends the fault, so
// its return alerts at once; another fault of the same channel is its own.
func TestAlertLasting_EndsWhenConnected(t *testing.T) {
	bc, rec, fc := newWatchedChannel(t)

	bc.AlertLasting(rejected())
	bc.AlertLasting(alerter.Alert{Title: "Telegram polling failed", Description: "test: 401"})
	if n := recorded(rec); n != 2 {
		t.Fatalf("two different faults must each alert, got %d", n)
	}

	fc.Advance(time.Minute)
	bc.ReportConnected()
	bc.AlertLasting(rejected())
	got := snapshot(rec)
	if len(got) != 3 || strings.Contains(got[2].Description, "ongoing") {
		t.Fatalf("a fault that returns after a working connection must alert as new, got %+v", got)
	}
}

// TestAlertLasting_WaitsForAnAlerter: a report nobody could hear does not
// count as the first alert.
func TestAlertLasting_WaitsForAnAlerter(t *testing.T) {
	bc := NewBaseChannel("test", nil, nil, nil)
	bc.AlertLasting(rejected()) // no alerter: must not panic, must not count

	rec := &alertRecorder{}
	bc.SetAlerter(rec)
	bc.AlertLasting(rejected())
	if got := snapshot(rec); len(got) != 1 || strings.Contains(got[0].Description, "ongoing") {
		t.Fatalf("the first report with an alerter must be the first alert, got %+v", got)
	}
}

// TestSendFailed_OnceThenDailyUntilDelivered: a channel whose sends keep
// failing alerts once and then once a day; a delivered send ends that, so the
// next failure alerts at once; another channel's failures are its own.
func TestSendFailed_OnceThenDailyUntilDelivered(t *testing.T) {
	m := newTestManager()
	fc := clock.NewFake(time.Date(2026, time.November, 14, 9, 2, 0, 0, time.UTC))
	m.clock = fc
	rec := &alertRecorder{}
	m.SetAlerter(rec)
	msg := bus.OutboundMessage{Channel: "test", ChatID: "1", Content: "hello"}

	fail := true
	w := &channelWorker{
		ch: &mockChannel{sendFn: func(context.Context, bus.OutboundMessage) error {
			if fail {
				return fmt.Errorf("rejected: %w", ErrSendFailed)
			}
			return nil
		}},
		limiter: rate.NewLimiter(rate.Inf, 1),
	}
	// send sends msg on channel and checks that it ended as fail says.
	send := func(channel string) {
		t.Helper()
		err := m.sendWithRetry(context.Background(), channel, w, msg)
		if fail != errors.Is(err, ErrSendFailed) || (!fail && err != nil) {
			t.Fatalf("sendWithRetry = %v with fail=%v", err, fail)
		}
	}

	for range 5 {
		send("test")
		fc.Advance(time.Hour)
	}
	if got := snapshot(rec); len(got) != 1 || got[0].EventID != "test" || strings.Contains(got[0].Description, "ongoing") {
		t.Fatalf("five failed sends must raise one alert, got %+v", got)
	}

	send("other")
	if got := snapshot(rec); len(got) != 2 || got[1].EventID != "other" {
		t.Fatalf("another channel's failure is its own alert, got %+v", got)
	}

	fc.Advance(alerts.RemindEvery - 5*time.Hour)
	send("test")
	got := snapshot(rec)
	if len(got) != 3 || !strings.HasSuffix(got[2].Description, "; ongoing since Nov 14 09:02, 4 more since the last alert") {
		t.Fatalf("a day later the channel must alert once more with the count, got %+v", got)
	}

	fail = false
	send("test")
	fail = true
	send("test")
	got = snapshot(rec)
	if len(got) != 4 || got[3].Title != "Channel send failed" || strings.Contains(got[3].Description, "ongoing") {
		t.Fatalf("a failure after a delivered send must alert as new, got %+v", got)
	}
}
