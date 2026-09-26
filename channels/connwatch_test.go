package channels

import (
	"errors"
	"testing"
	"time"
)

const testAlertAfter = 50 * time.Millisecond

func newWatchedChannel(t *testing.T) (*BaseChannel, *alertRecorder) {
	t.Helper()
	bc := NewBaseChannel("test", nil, nil, nil)
	rec := &alertRecorder{}
	bc.SetAlerter(rec)
	bc.conn.alertAfter = testAlertAfter
	bc.SetRunning(true)
	return bc, rec
}

func recorded(rec *alertRecorder) int {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	return len(rec.alerts)
}

// waitPastAlert sleeps long enough for an outage clock to have expired.
func waitPastAlert() { time.Sleep(4 * testAlertAfter) }

// A failure run that outlasts the window raises exactly one alert carrying
// the latest error, however many failures follow.
func TestConnWatch_ProlongedOutageAlertsOnce(t *testing.T) {
	bc, rec := newWatchedChannel(t)
	bc.ReportConnFailure(errors.New("first"))
	bc.ReportConnFailure(errors.New("latest"))
	waitPastAlert()
	bc.ReportConnFailure(errors.New("after alert"))
	waitPastAlert()

	if n := recorded(rec); n != 1 {
		t.Fatalf("want 1 alert, got %d", n)
	}
	a := rec.alerts[0]
	if a.Title != "Channel connection down" || a.EventID != "test" || a.Details != "latest" {
		t.Fatalf("unexpected alert %+v", a)
	}
}

// Recovery inside the window cancels the alert.
func TestConnWatch_RecoveryBeforeWindowDoesNotAlert(t *testing.T) {
	bc, rec := newWatchedChannel(t)
	bc.ReportConnFailure(errors.New("429"))
	bc.ReportConnected()
	waitPastAlert()
	if n := recorded(rec); n != 0 {
		t.Fatalf("recovered outage must not alert, got %d", n)
	}
}

// A connection that never fails never alerts.
func TestConnWatch_HealthyNeverAlerts(t *testing.T) {
	bc, rec := newWatchedChannel(t)
	bc.ReportConnected()
	waitPastAlert()
	if n := recorded(rec); n != 0 {
		t.Fatalf("healthy channel must not alert, got %d", n)
	}
}

// After recovery, a new outage gets its own alert.
func TestConnWatch_NewOutageAfterRecoveryAlertsAgain(t *testing.T) {
	bc, rec := newWatchedChannel(t)
	bc.ReportConnFailure(errors.New("one"))
	waitPastAlert()
	bc.ReportConnected()
	bc.ReportConnFailure(errors.New("two"))
	waitPastAlert()
	if n := recorded(rec); n != 2 {
		t.Fatalf("want an alert per outage (2), got %d", n)
	}
}

// A channel stopped mid-outage is not retrying, so it does not alert, and a
// later outage starts a fresh clock.
func TestConnWatch_StoppedChannelDoesNotAlert(t *testing.T) {
	bc, rec := newWatchedChannel(t)
	bc.ReportConnFailure(errors.New("down"))
	bc.SetRunning(false)
	waitPastAlert()
	if n := recorded(rec); n != 0 {
		t.Fatalf("stopped channel must not alert, got %d", n)
	}

	bc.SetRunning(true)
	bc.ReportConnFailure(errors.New("down again"))
	waitPastAlert()
	if n := recorded(rec); n != 1 {
		t.Fatalf("restarted channel's new outage must alert, got %d", n)
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
