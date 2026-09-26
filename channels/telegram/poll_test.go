package telegram

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mymmrac/telego"
	ta "github.com/mymmrac/telego/telegoapi"
	"github.com/stretchr/testify/require"

	"github.com/PivotLLM/ClawEh/channels"
)

func rateLimited(retryAfter int) error {
	return &ta.Error{
		ErrorCode:   429,
		Description: "Too Many Requests: retry after 5",
		Parameters:  &ta.ResponseParameters{RetryAfter: retryAfter},
	}
}

// A 429 waits the server's retry_after plus the padding, and does not advance
// the backoff; other failures back off from ConnRetryMin.
func TestPollRetryWait(t *testing.T) {
	wait, next := pollRetryWait(rateLimited(5), 0)
	require.Equal(t, 5*time.Second+channels.RetryAfterPadding, wait)
	require.Equal(t, 6*time.Second, wait, "retry after 5 must wait 6s")
	require.Zero(t, next, "retry_after must not advance the backoff")

	boom := errors.New("request call: internal server error: 502")
	wait, next = pollRetryWait(boom, 0)
	require.Equal(t, channels.ConnRetryMin, wait)
	require.Equal(t, wait, next)
	wait, _ = pollRetryWait(boom, next)
	require.Equal(t, 2*channels.ConnRetryMin, wait)
	wait, _ = pollRetryWait(boom, channels.ConnRetryMax)
	require.Equal(t, channels.ConnRetryMax, wait)

	// A 429 without retry_after falls back to the backoff.
	wait, _ = pollRetryWait(&ta.Error{ErrorCode: 429, Description: "Too Many Requests"}, 0)
	require.Equal(t, channels.ConnRetryMin, wait)
}

// Rate limits and server errors are retried without alerting; a rejected
// token alerts at once.
func TestPollFailed_AlertsOnlyOnRejectedToken(t *testing.T) {
	ch := newTestChannel(t, &stubCaller{})
	rec := &alertRecorder{}
	ch.SetAlerter(rec)

	ch.pollFailed(rateLimited(5), 6*time.Second)
	ch.pollFailed(errors.New("request call: internal server error: 502"), 2*time.Second)
	ch.pollFailed(&ta.Error{ErrorCode: 409, Description: "Conflict: terminated by other getUpdates request"}, 2*time.Second)
	require.Empty(t, rec.alerts, "retryable poll failures must not alert")

	ch.pollFailed(&ta.Error{ErrorCode: 401, Description: "Unauthorized"}, time.Minute)
	require.Len(t, rec.alerts, 1)
	require.Equal(t, "Telegram polling failed", rec.alerts[0].Title)
	require.Equal(t, "telegram", rec.alerts[0].EventID)
	require.Equal(t, `telegram: 401 "Unauthorized"`, rec.alerts[0].Description)
}

func updatesResponse(t *testing.T, ids ...int) *ta.Response {
	t.Helper()
	ups := make([]telego.Update, 0, len(ids))
	for _, id := range ids {
		ups = append(ups, telego.Update{UpdateID: id})
	}
	b, err := json.Marshal(ups)
	require.NoError(t, err)
	return &ta.Response{Ok: true, Result: b}
}

// The loop retries after a failure, delivers each update once in order even
// when Telegram repeats one, and exits promptly when cancelled mid-wait.
func TestPollUpdates_RetriesDeliversAndStops(t *testing.T) {
	var calls atomic.Int32
	caller := &stubCaller{callFn: func(ctx context.Context, _ string, _ *ta.RequestData) (*ta.Response, error) {
		switch calls.Add(1) {
		case 1:
			return nil, errors.New("internal server error: 502")
		case 2:
			return updatesResponse(t, 1, 2), nil
		case 3:
			return updatesResponse(t, 2, 3), nil
		default:
			return nil, errors.New("internal server error: 502")
		}
	}}
	ch := newTestChannel(t, caller)

	ctx, cancel := context.WithCancel(context.Background())
	updates := make(chan telego.Update, 10)
	done := make(chan struct{})
	go func() {
		defer close(done)
		ch.pollUpdates(ctx, updates)
	}()

	var got []int
	for len(got) < 3 {
		select {
		case u := <-updates:
			got = append(got, u.UpdateID)
		case <-time.After(3 * channels.ConnRetryMin):
			t.Fatalf("timed out; got updates %v", got)
		}
	}
	require.Equal(t, []int{1, 2, 3}, got)

	// The fourth call fails and the loop is now waiting to retry; cancelling
	// must end the wait at once rather than after it.
	require.Eventually(t, func() bool { return calls.Load() >= 4 }, time.Second, 5*time.Millisecond)
	start := time.Now()
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("pollUpdates did not exit after cancel")
	}
	require.Less(t, time.Since(start), channels.ConnRetryMin, "cancel must cut the retry wait short")

	_, open := <-updates
	require.False(t, open, "updates must be closed when the loop exits")
}
