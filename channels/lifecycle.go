// ClawEh
// License: MIT

package channels

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"time"

	"github.com/tenebris-tech/alerter"

	"github.com/PivotLLM/ClawEh/bus"
	"github.com/PivotLLM/ClawEh/logger"
)

const (
	// maxHeldPerChannel bounds the outbound messages held for one channel
	// while it starts; more are dropped as not running.
	maxHeldPerChannel = 1000
	// drainGrace is how long a stop still waits for a channel's workers once
	// it has cancelled them, before leaving them behind with a warning.
	drainGrace = 5 * time.Second
)

// StartAll starts the dispatchers and every channel, each in its own
// goroutine so a slow one delays no other, and returns once every channel has
// started or failed its first attempt (a failed one keeps retrying in the
// background, see retryChannelStart). Outbound messages to a channel still
// starting are held (up to maxHeldPerChannel) and sent, in order, once it has.
func (m *Manager) StartAll(ctx context.Context) error {
	m.reloadMu.Lock()
	defer m.reloadMu.Unlock()

	m.mu.Lock()
	if len(m.channels) == 0 {
		m.mu.Unlock()
		logger.WarnC("channels", "No channels enabled")
		return errors.New("no channels enabled")
	}

	logger.InfoC("channels", "Starting all channels")

	dispatchCtx, cancel := context.WithCancel(ctx)
	m.dispatchTask = &asyncTask{cancel: cancel}
	m.dispatchCtx = dispatchCtx
	toStart := make(map[string]Channel, len(m.channels))
	for name, ch := range m.channels {
		toStart[name] = ch
		m.held[name] = &heldOutbound{}
	}
	m.mu.Unlock()

	// Start the dispatchers that read from the bus and route to workers
	go m.dispatchOutbound(dispatchCtx)
	go m.dispatchOutboundMedia(dispatchCtx)

	// Start the TTL janitor that cleans up stale typing/placeholder entries
	go m.runTTLJanitor(dispatchCtx)

	m.startChannels(ctx, toStart)

	logger.InfoC("channels", "All channels started")
	return nil
}

// startChannels starts chs concurrently and returns once each has started or
// failed its first attempt.
func (m *Manager) startChannels(ctx context.Context, chs map[string]Channel) {
	var wg sync.WaitGroup
	for name, ch := range chs {
		wg.Go(func() { m.startChannel(ctx, name, ch) })
	}
	wg.Wait()
}

// startChannel makes a channel's first start attempt. ctx is handed to its
// Start.
func (m *Manager) startChannel(ctx context.Context, name string, channel Channel) {
	logger.InfoCF("channels", "Starting channel", map[string]any{
		"channel": name,
	})
	m.started(name, channel, channel.Start(ctx)) //nolint:contextcheck // the channel's workers and retries live as long as the dispatchers, not this call
}

// started follows a channel's first Start (err its result): a started channel
// gets its workers, which first send what was held for it; a failed one goes
// to retryChannelStart and what was held is dropped. Both run on a context of
// the channel's own, derived from the dispatchers' and cancelled when the
// channel is stopped. A channel stopped or replaced while it started is left
// to whoever did that.
func (m *Manager) started(name string, channel Channel, err error) {
	m.mu.Lock()
	text, media := m.takeHeldLocked(name)
	if m.dispatchCtx == nil || m.channels[name] != channel {
		m.mu.Unlock()
		dropHeld(name, text, media, "was stopped")
		return
	}
	chanCtx, cancel := context.WithCancel(m.dispatchCtx)
	m.cancels[name] = cancel
	var w *channelWorker
	if err == nil {
		// Lazily create worker only after channel starts successfully
		w = newChannelWorker(name, channel)
		w.backlog, w.mediaBacklog = text, media
		m.workers[name] = w
	}
	m.mu.Unlock()

	if err != nil {
		logger.ErrorCF("channels", "Failed to start channel — will retry in background", map[string]any{
			"channel": name,
			"error":   err.Error(),
		})
		dropHeld(name, text, media, "did not start")
		go m.retryChannelStart(chanCtx, name, channel)
		return
	}
	go m.runWorker(chanCtx, name, w)
	go m.runMediaWorker(chanCtx, name, w)
}

// retryChannelStart retries channel.Start in the background with exponential
// backoff (StartRetryMin doubling to StartRetryMax) until it succeeds or
// chanCtx is cancelled. A "Channel failed to start" alert is raised once,
// on the StartRetryAlertAfter-th failure; retries continue at the ceiling.
// On success it creates a worker and starts runWorker/runMediaWorker goroutines.
// chanCtx is the channel's context made by started; it is cancelled under
// m.mu when the channel is taken out of service (detachLocked), which is what
// keeps a late success from registering a worker for it.
func (m *Manager) retryChannelStart(chanCtx context.Context, name string, channel Channel) {
	backoff := StartRetryMin
	for attempt := 1; ; attempt++ {
		select {
		case <-chanCtx.Done():
			return
		case <-m.clk().After(jitter(backoff)):
		}

		logger.InfoCF("channels", "Retrying channel start", map[string]any{
			"channel": name,
			"attempt": attempt,
		})

		err := channel.Start(chanCtx)
		if err == nil {
			// Channel started — create and register the worker, unless it was
			// stopped or replaced meanwhile.
			w := newChannelWorker(name, channel)
			m.mu.Lock()
			current := chanCtx.Err() == nil && m.channels[name] == channel
			if current {
				m.workers[name] = w
			}
			m.mu.Unlock()
			if !current {
				// Stopped or replaced while this attempt ran: undo it.
				if stopErr := channel.Stop(context.WithoutCancel(chanCtx)); stopErr != nil {
					logger.WarnCF("channels", "Error stopping a channel started after it was removed", map[string]any{
						"channel": name,
						"error":   stopErr.Error(),
					})
				}
				return
			}

			go m.runWorker(chanCtx, name, w)
			go m.runMediaWorker(chanCtx, name, w)

			logger.InfoCF("channels", "Channel started after retries", map[string]any{
				"channel":  name,
				"attempts": attempt,
			})
			return
		}
		if chanCtx.Err() != nil {
			// Start failed because we are shutting down; not a fault.
			return
		}

		backoff = min(backoff*2, StartRetryMax)
		fields := map[string]any{
			"channel":  name,
			"attempt":  attempt,
			"error":    err.Error(),
			"retry_in": backoff.String(),
		}
		if attempt == StartRetryAlertAfter {
			logger.ErrorCF("channels", "Channel still failing to start; alerting and continuing to retry", fields)
			m.alert(alerter.Alert{
				Title:       "Channel failed to start",
				Description: name + " has failed to start " + strconv.Itoa(attempt) + " times; retrying every " + StartRetryMax.String() + " until it starts",
				Details:     err.Error(),
				EventID:     name,
			})
			continue
		}
		logger.WarnCF("channels", "Channel start retry failed", fields)
	}
}

// StopAll stops the dispatchers and every channel.
func (m *Manager) StopAll(ctx context.Context) error {
	m.reloadMu.Lock()
	defer m.reloadMu.Unlock()
	m.mu.Lock()
	defer m.mu.Unlock()

	logger.InfoC("channels", "Stopping all channels")

	// Cancel dispatcher, and with it every channel's workers and retries.
	if m.dispatchTask != nil {
		m.dispatchTask.cancel()
		m.dispatchTask = nil
	}
	m.dispatchCtx = nil
	clear(m.cancels)
	for name := range m.held {
		text, media := m.takeHeldLocked(name)
		dropHeld(name, text, media, "was stopped")
	}

	// Stop all channels in parallel under a single deadline. Telegram's Stop
	// can block up to its pollExitTimeout; running stops sequentially would
	// make total elapsed time the sum of every channel's worst case, which
	// stalls config reloads (see investigation 7a5377d9).
	stopCtx, cancel := context.WithTimeout(ctx, stopAllTimeout)
	defer cancel()

	// Close all worker queues and wait for them to drain
	for _, w := range m.workers {
		if w != nil {
			w.closeQueues()
		}
	}
	for name, w := range m.workers {
		if w != nil {
			waitWorker(stopCtx, name, w, nil)
		}
	}

	var wg sync.WaitGroup
	var errMu sync.Mutex
	var stopErrs []error
	for name, channel := range m.channels {
		wg.Go(func() {
			if err := stopOne(stopCtx, name, channel); err != nil {
				errMu.Lock()
				stopErrs = append(stopErrs, err)
				errMu.Unlock()
			}
		})
	}
	wg.Wait()

	logger.InfoC("channels", "All channels stopped")
	if len(stopErrs) > 0 {
		return errors.Join(stopErrs...)
	}
	return nil
}

// stopOne stops one channel, logging the outcome.
func stopOne(ctx context.Context, name string, channel Channel) error {
	logger.InfoCF("channels", "Stopping channel", map[string]any{
		"channel": name,
	})
	if err := channel.Stop(ctx); err != nil {
		logger.ErrorCF("channels", "Error stopping channel", map[string]any{
			"channel": name,
			"error":   err.Error(),
		})
		return fmt.Errorf("%s: %w", name, err)
	}
	return nil
}

// waitWorker waits for w's goroutines to finish what is queued and exit.
// Once ctx ends it calls cancel (when not nil), which makes them give up the
// rest, and waits drainGrace more; then it warns, naming the channel, and
// returns, leaving them behind.
func waitWorker(ctx context.Context, name string, w *channelWorker, cancel context.CancelFunc) {
	exited := make(chan struct{})
	go func() {
		<-w.done
		<-w.mediaDone
		close(exited)
	}()
	select {
	case <-exited:
		return
	case <-ctx.Done():
	}
	if cancel != nil {
		cancel()
	}
	select {
	case <-exited:
	case <-time.After(drainGrace):
		logger.WarnCF("channels", "Channel's outbound worker did not stop; leaving it behind", map[string]any{
			"channel": name,
		})
	}
}

// heldOutbound collects the outbound messages for a channel that is starting.
type heldOutbound struct {
	mu         sync.Mutex
	text       []bus.OutboundMessage
	media      []bus.OutboundMediaMessage
	overflowed bool
}

// hold appends msg to *list unless h is full; firstOverflow is true for the
// first message refused.
func hold[M any](h *heldOutbound, list *[]M, msg M) (held, firstOverflow bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.text)+len(h.media) >= maxHeldPerChannel {
		first := !h.overflowed
		h.overflowed = true
		return false, first
	}
	*list = append(*list, msg)
	return true, false
}

func holdText(h *heldOutbound, msg bus.OutboundMessage) (bool, bool) { return hold(h, &h.text, msg) }

func holdMedia(h *heldOutbound, msg bus.OutboundMediaMessage) (bool, bool) {
	return hold(h, &h.media, msg)
}

// takeHeldLocked ends the hold on channel name and returns what it held.
// m.mu must be held for writing: the dispatchers add to a hold under the
// read lock.
func (m *Manager) takeHeldLocked(name string) ([]bus.OutboundMessage, []bus.OutboundMediaMessage) {
	h := m.held[name]
	if h == nil {
		return nil, nil
	}
	delete(m.held, name)
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.text, h.media
}

// dropHeld reports held messages that will not be sent, because channel name
// "did not start" / "was stopped" (why).
func dropHeld(name string, text []bus.OutboundMessage, media []bus.OutboundMediaMessage, why string) {
	if len(text)+len(media) == 0 {
		return
	}
	err := fmt.Errorf("%w: %s %s", ErrNotRunning, name, why)
	for _, msg := range text {
		reportDelivery(msg, err)
	}
	logger.WarnCF("channels", "Dropped the messages held for a channel that is not running", map[string]any{
		"channel":  name,
		"reason":   why,
		"messages": len(text) + len(media),
	})
}
