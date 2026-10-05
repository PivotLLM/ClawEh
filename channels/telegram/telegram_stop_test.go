package telegram

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/PivotLLM/ClawEh/bus"
	"github.com/PivotLLM/ClawEh/channels"
	"github.com/PivotLLM/ClawEh/config"
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

// TestStopAbortsInFlightLongPoll is the regression test for the 10 s stop: a
// getUpdates long poll the server never answers must be abandoned as soon as
// Stop cancels the channel, not after pollExitTimeout.
func TestStopAbortsInFlightLongPoll(t *testing.T) {
	orig := pollExitTimeout
	pollExitTimeout = 5 * time.Second
	t.Cleanup(func() { pollExitTimeout = orig })

	polling := make(chan struct{}, 1)
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/getUpdates"):
			select {
			case polling <- struct{}{}:
			default:
			}
			// Never answered while the client is connected.
			select {
			case <-r.Context().Done():
			case <-release:
			}
		case strings.HasSuffix(r.URL.Path, "/getMe"):
			if _, err := w.Write([]byte(`{"ok":true,"result":{"id":1,"is_bot":true,"first_name":"t","username":"t_bot"}}`)); err != nil {
				t.Errorf("write getMe: %v", err)
			}
		default:
			if _, err := w.Write([]byte(`{"ok":true,"result":true}`)); err != nil {
				t.Errorf("write %s: %v", r.URL.Path, err)
			}
		}
	}))
	t.Cleanup(func() {
		close(release)
		srv.Close()
	})

	ch, err := NewTelegramChannelFromConfig(config.TelegramBotConfig{Token: testToken, BaseURL: srv.URL}, bus.NewMessageBus())
	require.NoError(t, err)
	require.NoError(t, ch.Start(context.Background()))

	select {
	case <-polling:
	case <-time.After(5 * time.Second):
		t.Fatal("getUpdates never reached the fake server")
	}

	start := time.Now()
	require.NoError(t, ch.Stop(context.Background()))
	require.Less(t, time.Since(start), time.Second, "Stop waited for the long poll instead of aborting it")
}
