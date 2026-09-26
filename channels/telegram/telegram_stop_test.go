package telegram

import (
	"context"
	"testing"
	"time"

	"github.com/PivotLLM/ClawEh/channels"
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
		if err := c.Stop(context.Background()); err != nil {
			t.Errorf("Stop: %v", err)
		}
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
	if err := c.Stop(context.Background()); err != nil {
		t.Fatalf("Stop: %v", err)
	}
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

	for i := range 3 {
		done := make(chan struct{})
		go func() {
			if err := c.Stop(context.Background()); err != nil {
				t.Errorf("Stop: %v", err)
			}
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(1 * time.Second):
			t.Fatalf("Stop() call %d hung", i+1)
		}
	}
}
