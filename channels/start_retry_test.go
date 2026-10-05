// ClawEh
// License: MIT

package channels

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/PivotLLM/ClawEh/bus"
	"github.com/PivotLLM/ClawEh/internal/testalerts"
)

// flakyStartChannel fails Start the first failures times, then succeeds.
type flakyStartChannel struct {
	mockChannel
	failures int
	attempts atomic.Int32
}

func (c *flakyStartChannel) Start(context.Context) error {
	if int(c.attempts.Add(1)) <= c.failures {
		return errors.New("service unreachable")
	}
	return nil
}

// noWait replaces startRetryAfter for the test: every wait completes at once
// and the requested durations are recorded.
func noWait(t *testing.T) func() []time.Duration {
	t.Helper()
	var mu sync.Mutex
	var waits []time.Duration
	prev := startRetryAfter
	startRetryAfter = func(d time.Duration) <-chan time.Time {
		mu.Lock()
		waits = append(waits, d)
		mu.Unlock()
		ch := make(chan time.Time, 1)
		ch <- time.Time{}
		return ch
	}
	t.Cleanup(func() { startRetryAfter = prev })
	return func() []time.Duration {
		mu.Lock()
		defer mu.Unlock()
		return append([]time.Duration(nil), waits...)
	}
}

// TestRetryChannelStart_KeepsRetryingPastAlert: the manager never gives up on a
// channel that will not start. It alerts once on the StartRetryAlertAfter-th
// failure, keeps retrying at the backoff ceiling, and registers the worker
// when Start eventually succeeds.
func TestRetryChannelStart_KeepsRetryingPastAlert(t *testing.T) {
	waits := noWait(t)
	m := newTestManager()
	rec := &testalerts.Recorder{}
	m.SetAlerter(rec)

	const failures = StartRetryAlertAfter + 1 // succeeds on attempt 12
	ch := &flakyStartChannel{failures: failures}
	ch.sendFn = func(context.Context, bus.OutboundMessage) error { return nil }
	m.RegisterChannel("flaky", ch)

	ctx, cancel := context.WithCancel(context.Background())
	m.retryChannelStart(ctx, "flaky", ch)

	if got := ch.attempts.Load(); got != failures+1 {
		t.Fatalf("Start attempts = %d, want %d", got, failures+1)
	}
	m.mu.RLock()
	w := m.workers["flaky"]
	m.mu.RUnlock()
	if w == nil {
		t.Fatal("worker not registered after the channel started")
	}

	alerts := rec.Alerts()
	if len(alerts) != 1 {
		t.Fatalf("alerts = %d, want exactly 1: %+v", len(alerts), alerts)
	}
	if alerts[0].Title != "Channel failed to start" || alerts[0].EventID != "flaky" {
		t.Fatalf("unexpected alert %+v", alerts[0])
	}

	ws := waits()
	if len(ws) != failures+1 {
		t.Fatalf("waits = %d, want %d", len(ws), failures+1)
	}
	// The first wait is StartRetryMin; by the alert attempt the backoff has
	// reached StartRetryMax and stays there. Each wait carries +/-20% jitter.
	within := func(d, want time.Duration) bool {
		return d >= time.Duration(float64(want)*(1-backoffJitter)) && d <= time.Duration(float64(want)*(1+backoffJitter))
	}
	if !within(ws[0], StartRetryMin) {
		t.Errorf("first wait %v, want ~%v", ws[0], StartRetryMin)
	}
	for i := StartRetryAlertAfter - 1; i < len(ws); i++ {
		if !within(ws[i], StartRetryMax) {
			t.Errorf("wait %d = %v, want ~%v (ceiling)", i+1, ws[i], StartRetryMax)
		}
	}

	// Shut the workers down.
	cancel()
	close(w.queue)
	close(w.mediaQueue)
	<-w.done
	<-w.mediaDone
}

// TestRetryChannelStart_StopsOnCancel: cancelling the dispatch context ends the
// loop without an alert, however many times Start has failed.
func TestRetryChannelStart_StopsOnCancel(t *testing.T) {
	noWait(t)
	m := newTestManager()

	ctx, cancel := context.WithCancel(context.Background())
	ch := &flakyStartChannel{failures: 1 << 30}
	m.RegisterChannel("dead", ch)

	done := make(chan struct{})
	go func() {
		defer close(done)
		m.retryChannelStart(ctx, "dead", ch)
	}()
	// Let it fail a few times, then cancel.
	deadline := time.After(5 * time.Second)
	for ch.attempts.Load() < 3 {
		select {
		case <-deadline:
			t.Fatal("retry loop did not run")
		default:
			time.Sleep(time.Millisecond)
		}
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("retry loop did not stop on cancel")
	}
	m.mu.RLock()
	_, hasWorker := m.workers["dead"]
	m.mu.RUnlock()
	if hasWorker {
		t.Fatal("worker registered for a channel that never started")
	}
}
