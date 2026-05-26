package telegram

import (
	"context"
	"testing"
	"time"

	"github.com/mymmrac/telego"

	"github.com/PivotLLM/ClawEh/pkg/channels"
)

// TestStopBlocksUntilLongPollExits is the regression test for the telegram-409
// reload race. If the wait on pollDone is removed from Stop(), this test fails
// because Stop() returns immediately, before the simulated long-poll
// goroutine has signalled exit.
func TestStopBlocksUntilLongPollExits(t *testing.T) {
	base := channels.NewBaseChannel("telegram", nil, nil, nil)
	_, cancel := context.WithCancel(context.Background())
	pollDone := make(chan struct{})
	c := &TelegramChannel{
		BaseChannel: base,
		ctx:         context.Background(),
		cancel:      cancel,
		pollDone:    pollDone,
		chatIDs:     map[string]int64{},
	}

	returned := make(chan struct{})
	go func() {
		_ = c.Stop(context.Background())
		close(returned)
	}()

	select {
	case <-returned:
		t.Fatal("Stop() returned before long-poll goroutine signalled done")
	case <-time.After(100 * time.Millisecond):
	}

	close(pollDone)

	select {
	case <-returned:
	case <-time.After(2 * time.Second):
		t.Fatal("Stop() did not return after long-poll signalled done")
	}
}

// TestStopHonoursTimeout proves a stuck long-poll cannot deadlock Stop()
// forever — after pollExitTimeout, Stop() returns and logs a WRN.
func TestStopHonoursTimeout(t *testing.T) {
	orig := pollExitTimeout
	pollExitTimeout = 50 * time.Millisecond
	t.Cleanup(func() { pollExitTimeout = orig })

	base := channels.NewBaseChannel("telegram", nil, nil, nil)
	_, cancel := context.WithCancel(context.Background())
	c := &TelegramChannel{
		BaseChannel: base,
		ctx:         context.Background(),
		cancel:      cancel,
		pollDone:    make(chan struct{}), // never closed
		chatIDs:     map[string]int64{},
	}

	start := time.Now()
	_ = c.Stop(context.Background())
	elapsed := time.Since(start)

	if elapsed < pollExitTimeout {
		t.Fatalf("Stop() returned before timeout: %s < %s", elapsed, pollExitTimeout)
	}
	if elapsed > pollExitTimeout+500*time.Millisecond {
		t.Fatalf("Stop() returned too late: %s", elapsed)
	}
}

// TestStopIsIdempotent ensures repeated Stop() calls don't double-close
// channels or otherwise deadlock.
func TestStopIsIdempotent(t *testing.T) {
	base := channels.NewBaseChannel("telegram", nil, nil, nil)
	_, cancel := context.WithCancel(context.Background())
	pollDone := make(chan struct{})
	close(pollDone)
	c := &TelegramChannel{
		BaseChannel: base,
		ctx:         context.Background(),
		cancel:      cancel,
		pollDone:    pollDone,
		chatIDs:     map[string]int64{},
	}

	for i := 0; i < 3; i++ {
		done := make(chan struct{})
		go func() {
			_ = c.Stop(context.Background())
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(1 * time.Second):
			t.Fatalf("Stop() call %d hung", i+1)
		}
	}
}

// TestWatchLongPollDoneClosesOnSrcClose verifies the wrapping primitive:
// done must not close until src is closed, regardless of ctx state.
func TestWatchLongPollDoneClosesOnSrcClose(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	src := make(chan telego.Update)
	_, done := watchLongPoll(ctx, src)

	// done must not close yet
	select {
	case <-done:
		t.Fatal("done closed before src closed")
	case <-time.After(50 * time.Millisecond):
	}

	// Cancelling ctx alone must NOT close done — the doLongPolling
	// goroutine may still be mid-getUpdates.
	cancel()
	select {
	case <-done:
		t.Fatal("done closed on ctx cancel — must wait for src close")
	case <-time.After(50 * time.Millisecond):
	}

	// Closing src (simulating doLongPolling's defer) must close done.
	close(src)
	select {
	case <-done:
	case <-time.After(1 * time.Second):
		t.Fatal("done did not close after src closed")
	}
}

// TestWatchLongPollDrainsAfterCtxCancel verifies that once ctx is cancelled
// and the downstream consumer has stopped reading, the relay drains src so
// telego's doLongPolling can finish sending and close.
func TestWatchLongPollDrainsAfterCtxCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())

	src := make(chan telego.Update)
	_, done := watchLongPoll(ctx, src)

	cancel()

	// Producer can still push and then close — relay must drain, not block.
	pushed := make(chan struct{})
	go func() {
		defer close(pushed)
		for i := 0; i < 5; i++ {
			select {
			case src <- telego.Update{UpdateID: i}:
			case <-time.After(500 * time.Millisecond):
				t.Errorf("producer blocked on src send %d — relay did not drain", i)
				return
			}
		}
		close(src)
	}()

	select {
	case <-pushed:
	case <-time.After(2 * time.Second):
		t.Fatal("producer never finished")
	}

	select {
	case <-done:
	case <-time.After(1 * time.Second):
		t.Fatal("done did not close after src closed")
	}
}
