// ClawEh
// License: MIT
//
// Copyright (c) 2026 Tenebris Technologies Inc.

package schedule

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/PivotLLM/ClawEh/cron"
	"github.com/PivotLLM/ClawEh/cronmsg"
	"github.com/PivotLLM/ClawEh/logger"
)

// A listen job turns a long-poll tool into a persistent callback. Where a
// scheduled watch calls its tool on a timer and compares, a listen job calls
// the tool, waits for it to return (an event, a dropped connection, or the
// timeout), delivers the full result to the agent when the watched fields carry
// something new, and immediately calls it again. It runs in its own goroutine
// for as long as the job is enabled, so a tool such as documents_event_wait
// becomes a standing notification channel into the agent with no model in the
// loop until there is something to say.
const (
	// listenDefaultTimeout bounds one call when the job sets none.
	listenDefaultTimeout = 5 * time.Minute
	// listenBackoffMax caps the retry delay after failed calls.
	listenBackoffMax = 5 * time.Minute
)

// Timing levers, variables so tests can shorten them.
var (
	// listenMinInterval is the floor between consecutive calls, so a tool that
	// returns instantly with nothing cannot spin the loop.
	listenMinInterval = 2 * time.Second
	// listenBackoffBase is the delay after the first failed call; it doubles
	// per consecutive failure up to listenBackoffMax.
	listenBackoffBase = 5 * time.Second
	// listenReconcileInterval is how often the supervisor compares the running
	// listeners with the enabled listen jobs.
	listenReconcileInterval = 10 * time.Second
)

// listener is one running listen loop.
type listener struct {
	cancel context.CancelFunc
	done   chan struct{}
}

// listenKey identifies a listener by what it does, so an edited job (new tool,
// arguments, fields or message) restarts and an untouched one keeps running.
func listenKey(j *cron.CronJob) string {
	return j.ID + ":" + j.AgentID + ":" + j.Fingerprint
}

// isListenJob reports whether a job should have a running listener.
func isListenJob(j *cron.CronJob) bool {
	return j.Enabled && j.Schedule.Kind == cron.KindListen && j.Payload.Watch != nil && j.AgentID != ""
}

// StartListeners starts the supervisor that keeps one goroutine per enabled
// listen job. It reconciles immediately and then every listenReconcileInterval,
// so jobs added, edited, disabled or removed through the tool, the CLI or a
// config reload take effect within that interval. Idempotent.
func (t *CronTool) StartListeners(ctx context.Context) {
	t.listenMu.Lock()
	defer t.listenMu.Unlock()
	if t.listenStop != nil {
		return
	}
	if t.listeners == nil {
		t.listeners = make(map[string]*listener)
	}
	supCtx, cancel := context.WithCancel(ctx)
	t.listenStop = cancel
	if t.listenKick == nil {
		t.listenKick = make(chan struct{}, 1)
	}
	t.listenWG.Go(func() {
		t.reconcileListeners(supCtx)
		ticker := time.NewTicker(listenReconcileInterval)
		defer ticker.Stop()
		for {
			select {
			case <-supCtx.Done():
				return
			case <-ticker.C:
				t.reconcileListeners(supCtx)
			case <-t.listenKick:
				t.reconcileListeners(supCtx)
			}
		}
	})
}

// kickListeners asks the supervisor to reconcile now rather than at the next
// tick. The tool calls it after add/remove/enable/disable so a listener starts
// or stops as soon as the job changes; the ticker still covers edits made by
// the CLI or a config reload. Non-blocking, and a no-op when not started.
func (t *CronTool) kickListeners() {
	t.listenMu.Lock()
	kick := t.listenKick
	started := t.listenStop != nil
	t.listenMu.Unlock()
	if !started || kick == nil {
		return
	}
	select {
	case kick <- struct{}{}:
	default: // a reconcile is already pending
	}
}

// StopListeners stops the supervisor and every listener and waits for them.
func (t *CronTool) StopListeners() {
	t.listenMu.Lock()
	if t.listenStop == nil {
		t.listenMu.Unlock()
		return
	}
	t.listenStop()
	t.listenStop = nil
	for key, l := range t.listeners {
		l.cancel()
		delete(t.listeners, key)
	}
	t.listenMu.Unlock()
	t.listenWG.Wait()
}

// reconcileListeners starts a listener for every enabled listen job that has
// none and stops every listener whose job is gone, disabled or changed.
func (t *CronTool) reconcileListeners(ctx context.Context) {
	if t.cronService == nil {
		return
	}
	desired := make(map[string]cron.CronJob)
	for _, j := range t.cronService.ListJobs(true) {
		if isListenJob(&j) {
			desired[listenKey(&j)] = j
		}
	}

	t.listenMu.Lock()
	defer t.listenMu.Unlock()
	if t.listenStop == nil {
		return // stopped between the tick and the lock
	}
	for key, l := range t.listeners {
		if _, keep := desired[key]; !keep {
			l.cancel()
			delete(t.listeners, key)
		}
	}
	for key, job := range desired {
		if _, running := t.listeners[key]; running {
			continue
		}
		jobCtx, cancel := context.WithCancel(ctx)
		l := &listener{cancel: cancel, done: make(chan struct{})}
		t.listeners[key] = l
		t.listenWG.Add(1)
		go func(job cron.CronJob) {
			defer t.listenWG.Done()
			defer close(l.done)
			t.runListener(jobCtx, &job)
		}(job)
	}
}

// runListener is one listen job's loop. It ends only when ctx is cancelled.
func (t *CronTool) runListener(ctx context.Context, job *cron.CronJob) {
	w := job.Payload.Watch
	timeout := listenDefaultTimeout
	if w.TimeoutSec > 0 {
		timeout = time.Duration(w.TimeoutSec) * time.Second
	}
	fields := map[string]any{"id": job.ID, "agent_id": job.AgentID, "tool": w.Tool, "timeout": timeout.String()}
	logger.InfoCF("cron", "listen: started", fields)
	defer logger.InfoCF("cron", "listen: stopped", fields)

	// Every result with the watched fields present is an event in its own
	// right and is delivered. A job that opted into suppression compares the
	// fingerprint with the last delivered one, which survives a restart
	// through the job state, so a tool that replays its most recent event on
	// reconnect does not deliver it twice.
	lastDigest := job.State.WatchDigest
	failures := job.State.WatchFailures

	for {
		if ctx.Err() != nil {
			return
		}
		started := time.Now()
		callCtx, cancel := context.WithTimeout(ctx, timeout)
		result, err := t.probeWithContext(callCtx, job.AgentID, w)
		timedOut := errors.Is(callCtx.Err(), context.DeadlineExceeded)
		cancel()
		if ctx.Err() != nil {
			return
		}

		switch {
		case err != nil && timedOut:
			// The wait ran out with nothing to report. Not a failure: call again.
			logger.DebugCF("cron", "listen: wait timed out, reconnecting", fields)
			if failures != 0 {
				failures = 0
				t.persistListenState(job, lastDigest, failures)
			}
		case err != nil:
			failures++
			t.persistListenState(job, lastDigest, failures)
			logger.WarnCF("cron", "listen: call failed", map[string]any{
				"id": job.ID, "tool": w.Tool, "failures": failures, "error": err.Error(),
			})
			if failures == watchFailureNotifyThreshold {
				if _, derr := t.deliver(ctx, job, cronmsg.BuildEvent(time.Now(), fmt.Sprintf(
					"Listener %q has failed %d times in a row calling %q and is not receiving events. Last error: %v",
					job.Name, failures, w.Tool, err), w.Tool, "")); derr != nil {
					logger.WarnCF("cron", "listen: failure notice not delivered", map[string]any{
						"id": job.ID, "tool": w.Tool, "reason": derr.Error(),
					})
				}
			}
			if !sleepCtx(ctx, listenBackoff(failures)) {
				return
			}
			continue
		default:
			if failures != 0 {
				failures = 0
				t.persistListenState(job, lastDigest, failures)
			}
			if digest, ok := listenEvent(result, w.Fields); ok && (!w.SuppressRepeats || digest != lastDigest) {
				// Advance the fingerprint only once the event is on the bus. A
				// delivery that fails (bus closed, or full for five seconds) is
				// logged and the event stays undelivered, so a source that
				// replays it on the next call gets it through.
				if _, derr := t.deliver(ctx, job, cronmsg.BuildEvent(time.Now(), job.Payload.Message, w.Tool, result)); derr != nil {
					logger.WarnCF("cron", "listen: event not delivered", map[string]any{
						"id": job.ID, "tool": w.Tool, "reason": derr.Error(),
					})
				} else {
					lastDigest = digest
					t.persistListenState(job, lastDigest, 0)
					logger.InfoCF("cron", "listen: event delivered", fields)
				}
			} else {
				logger.DebugCF("cron", "listen: no new event", fields)
			}
		}

		// Never hammer a tool that returns immediately.
		if rest := listenMinInterval - time.Since(started); rest > 0 {
			if !sleepCtx(ctx, rest) {
				return
			}
		}
	}
}

// listenEvent decides whether a result carries an event and fingerprints it.
// Unlike a scheduled watch, an absent watched field is "no data" rather than a
// change: a long-poll that returns empty-handed must not wake the agent. With
// no fields configured, any non-empty result is an event.
func listenEvent(result string, fields []string) (string, bool) {
	if result == "" {
		return "", false
	}
	snap := buildSnapshot(result, fields)
	for _, f := range fields {
		if snap[f] == nil {
			return "", false
		}
	}
	digest, err := snap.digest()
	if err != nil {
		return "", false
	}
	return digest, true
}

// listenBackoff returns the delay before retrying after n consecutive failures.
func listenBackoff(n int) time.Duration {
	d := listenBackoffBase
	for i := 1; i < n && d < listenBackoffMax; i++ {
		d *= 2
	}
	if d > listenBackoffMax {
		d = listenBackoffMax
	}
	return d
}

// sleepCtx waits for d or until ctx ends; false means ctx ended.
func sleepCtx(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

// persistListenState records the digest and failure count on the job so they
// survive a restart. Best-effort: a save failure is logged, not fatal.
func (t *CronTool) persistListenState(job *cron.CronJob, digest string, failures int) {
	if err := t.cronService.UpdateWatchState(job.ID, digest, failures); err != nil {
		logger.WarnCF("cron", "listen: could not persist state", map[string]any{"id": job.ID, "error": err.Error()})
	}
}
