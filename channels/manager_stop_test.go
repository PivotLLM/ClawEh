package channels

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/time/rate"

	"github.com/PivotLLM/ClawEh/bus"
)

// slowStopChannel is a mockChannel whose Stop, when release is set, reports
// on entered and returns only once release is closed. Used to detect
// whether StopAll runs Stop calls concurrently or serially — see
// TestStopAll_RunsChannelStopsConcurrently.
type slowStopChannel struct {
	mockChannel
	entered chan<- struct{}
	release <-chan struct{}
	stopErr error
	stopped atomic.Int32
}

func (s *slowStopChannel) Stop(ctx context.Context) error {
	if s.release != nil {
		s.entered <- struct{}{}
		select {
		case <-s.release:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	s.stopped.Add(1)
	return s.stopErr
}

// TestStopAll_RunsChannelStopsConcurrently exercises StopAll with three
// channels whose Stop blocks until all three have been entered. Run
// serially, the first Stop would never return and the others never start;
// run concurrently, all three are in Stop at once.
func TestStopAll_RunsChannelStopsConcurrently(t *testing.T) {
	const n = 3
	entered := make(chan struct{}, n)
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseAll := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(releaseAll) // a failed run leaves no Stop blocked

	mgr := newTestManager()

	channels := make([]*slowStopChannel, n)
	for i := range n {
		ch := &slowStopChannel{
			sendFn:  func(_ context.Context, _ bus.OutboundMessage) error { return nil },
			entered: entered,
			release: release,
		}
		channels[i] = ch
		name := fmt.Sprintf("slow-%d", i)
		mgr.channels[name] = ch
		// Register a worker too so StopAll's worker drain path executes — this
		// mirrors a real Manager that has at least started one worker.
		mgr.workers[name] = &channelWorker{
			ch:         ch,
			queue:      make(chan bus.OutboundMessage),
			mediaQueue: make(chan bus.OutboundMediaMessage),
			done:       make(chan struct{}),
			mediaDone:  make(chan struct{}),
			limiter:    rate.NewLimiter(rate.Inf, 1),
		}
		// Spawn drain goroutines so close(queue) terminates and done fires.
		go mgr.runWorker(context.Background(), name, mgr.workers[name])
		go mgr.runMediaWorker(context.Background(), name, mgr.workers[name])
	}

	result := make(chan error, 1)
	go func() { result <- mgr.StopAll(context.Background()) }()
	for i := range n {
		select {
		case <-entered:
		case <-time.After(10 * time.Second):
			t.Fatalf("only %d of %d channels were in Stop at once; StopAll stops them serially", i, n)
		}
	}
	releaseAll()
	if err := <-result; err != nil {
		t.Fatalf("StopAll returned error: %v", err)
	}

	// Sanity: every channel should have observed its Stop call. If one didn't,
	// the elapsed bound test could pass for the wrong reason (channels skipped).
	for i, ch := range channels {
		if got := ch.stopped.Load(); got != 1 {
			t.Fatalf("channel %d: stopped=%d, want 1", i, got)
		}
	}
}

// TestStopAll_CollectsAllChannelErrors verifies that a per-channel Stop error
// does not short-circuit StopAll — every channel still gets stopped and the
// errors are joined into the return value. Required by option #2 ("collect any
// per-channel errors into a slice and log them; don't return on first error").
func TestStopAll_CollectsAllChannelErrors(t *testing.T) {
	mgr := newTestManager()

	errA := errors.New("a failed")
	errB := errors.New("b failed")

	chA := &slowStopChannel{stopErr: errA}
	chB := &slowStopChannel{stopErr: errB}
	chC := &slowStopChannel{}

	mgr.channels["a"] = chA
	mgr.channels["b"] = chB
	mgr.channels["c"] = chC

	err := mgr.StopAll(context.Background())
	if err == nil {
		t.Fatal("StopAll returned nil with two failing channels; want joined error")
	}
	if !errors.Is(err, errA) {
		t.Errorf("StopAll error missing errA: %v", err)
	}
	if !errors.Is(err, errB) {
		t.Errorf("StopAll error missing errB: %v", err)
	}
	if got := chC.stopped.Load(); got != 1 {
		t.Errorf("non-failing channel c: stopped=%d, want 1 (a failure must not short-circuit StopAll)", got)
	}
}

// TestStopTyping_StopsAndRemoves verifies StopTyping invokes the registered stop
// function and clears the entry so a later send does not double-stop.
func TestStopTyping_StopsAndRemoves(t *testing.T) {
	m := newTestManager()

	calls := 0
	m.RecordTypingStop("test", "123", func() { calls++ })

	m.StopTyping("test", "123")
	if calls != 1 {
		t.Fatalf("expected stop func called once, got %d", calls)
	}

	// Entry removed: a second StopTyping is a no-op.
	m.StopTyping("test", "123")
	if calls != 1 {
		t.Fatalf("second StopTyping should be a no-op, calls=%d", calls)
	}
}

// TestStopTyping_Unregistered is a no-op (no panic) when nothing is registered.
func TestStopTyping_Unregistered(t *testing.T) {
	m := newTestManager()
	m.StopTyping("nope", "0") // must not panic
}
