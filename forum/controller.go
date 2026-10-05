// ClawEh
// License: MIT
//
// Copyright (c) 2026 Tenebris Technologies Inc.

package forum

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

// Seam (d): the controller (spec §5, §6, §2.3, §8 restart). A Controller
// executes one forum from its on-disk state. It is built only by Open
// (recover.go), whether the forum was launched a moment ago or is being
// resumed after a restart: there is one code path, driven by what the
// store already holds.
//
// Run is idempotent over the store. It walks the enabled layers in
// snapshot order, skips every layer, round and turn that already has a
// commit, and performs the first action that does not. A turn whose
// latest attempt has no reply is resent (§8). Nothing is kept in memory
// that is not also on disk before the next dispatch.

// Controller runs one forum.
type Controller struct {
	store *Store
	cfg   *Config
	snap  *Snapshot
	parts *Participants
	host  Host
	// schemas are the named schemas; decisionSchemas the effective
	// moderator schema per layer (Snapshot.ModeratorSchemas), both
	// compiled at Open.
	schemas         map[string]CompiledSchema
	decisionSchemas map[string]CompiledSchema
	router          *Router

	// mu guards state and attempts; every state mutation goes through
	// commit, every attempts mutation through ask.
	mu    sync.Mutex
	state *State
	// attempts caches Store.ListAttempts per layer at Open and is updated
	// as requests and replies are written.
	attempts map[string][]AttemptRecord
	// gone lists the created participants Open found missing (§8); Run
	// ends the forum failed when it is non-empty.
	gone []string

	// pause and cancel are set by RequestPause/RequestCancel and polled
	// between dispatches; cancel also cancels in-flight asks through
	// cancelActive.
	pause        atomic.Bool
	cancel       atomic.Bool
	cancelActive context.CancelFunc
}

// Run drives the forum until it is terminal or paused and returns the
// resulting status. Forum-level failures (exhausted attempts, a gone
// participant, limits) become a terminal state and a nil error; a non-nil
// error means the store refused a write, in which case the forum is left
// as it is on disk and a later Open resumes it.
//
// Sequence: finish an interrupted transition (StatusPausing -> paused,
// StatusCancelling -> cancelled); then for each enabled layer not Ended,
// runLayer; then end with StatusCompleted. A pause or cancel requested
// while a layer runs is honoured between dispatches (interrupted).
func (c *Controller) Run(ctx context.Context) (Status, error) {
	switch st := c.State(); st.Status {
	case StatusCompleted, StatusIncomplete, StatusFailed, StatusCancelled:
		return st.Status, nil
	case StatusPausing:
		return c.finishPause()
	case StatusCancelling:
		return c.finishCancel()
	case StatusQueued, StatusRunning, StatusPaused:
	}
	if len(c.gone) > 0 {
		c.host.Logger.Errorf("forum %s: temporary participants gone: %v", c.snap.ForumID, c.gone)
		return StatusFailed, c.end(StatusFailed, EndParticipantGone)
	}
	for _, id := range c.snap.Layers {
		layer, ok := c.cfg.Layer(id)
		if !ok {
			return "", fmt.Errorf("%w: snapshot layer %q is not configured", ErrCorrupt, id)
		}
		if c.layerState(id).Ended {
			continue
		}
		reason, err := c.runLayer(ctx, layer)
		if err != nil {
			return "", err
		}
		if c.pause.Load() {
			return c.finishPause()
		}
		if c.cancel.Load() {
			return c.finishCancel()
		}
		switch reason {
		case EndForumCallLimit, EndDeadline:
			return StatusIncomplete, c.end(StatusIncomplete, reason)
		case EndAttemptsExhausted, EndModeratorFailed, EndParticipantGone, EndHostError:
			return StatusFailed, c.end(StatusFailed, reason)
		case EndCompleted, EndRoundLimit, EndCallLimit, EndModeratorStop, EndCancelled:
		}
	}
	return StatusCompleted, c.end(StatusCompleted, EndCompleted)
}

// RequestPause asks the run to stop dispatching: it commits
// CommitPauseRequested (status pausing), lets in-flight asks finish within
// their timeouts, and the run then commits CommitPaused and returns. It is
// ErrInvalidState unless the status is running.
func (c *Controller) RequestPause() error {
	if c.State().Status != StatusRunning {
		return ErrInvalidState
	}
	if err := c.commit(&Commit{Kind: CommitPauseRequested, Status: StatusPausing}); err != nil {
		return err
	}
	c.pause.Store(true)
	return nil
}

// RequestCancel commits CommitCancelRequested (status cancelling), cancels
// in-flight asks, and the run then ends with StatusCancelled, keeping every
// committed output. It is ErrInvalidState when the status is terminal.
func (c *Controller) RequestCancel() error {
	if c.State().Status.Terminal() {
		return ErrInvalidState
	}
	if err := c.commit(&Commit{Kind: CommitCancelRequested, Status: StatusCancelling}); err != nil {
		return err
	}
	c.cancel.Store(true)
	c.mu.Lock()
	cancelActive := c.cancelActive
	c.mu.Unlock()
	if cancelActive != nil {
		cancelActive()
	}
	return nil
}

// State returns a copy of the current derived state.
func (c *Controller) State() State {
	c.mu.Lock()
	defer c.mu.Unlock()
	return *c.state
}

// Snapshot returns the launch snapshot.
func (c *Controller) Snapshot() *Snapshot { return c.snap }

// Config returns the configuration as launched.
func (c *Controller) Config() *Config { return c.cfg }

// Participants returns participants.json as loaded.
func (c *Controller) Participants() *Participants { return c.parts }

// finishPause completes a pause once no dispatch is in flight.
func (c *Controller) finishPause() (Status, error) {
	return StatusPaused, c.commit(&Commit{Kind: CommitPaused, Status: StatusPaused})
}

// finishCancel ends a cancelling forum.
func (c *Controller) finishCancel() (Status, error) {
	return StatusCancelled, c.end(StatusCancelled, EndCancelled)
}

// layerState returns a copy of a layer's state (zero when none yet).
func (c *Controller) layerState(layerID string) LayerState {
	c.mu.Lock()
	defer c.mu.Unlock()
	if ls := c.state.Layers[layerID]; ls != nil {
		return *ls
	}
	return LayerState{}
}

// runLayer executes one layer from wherever its state says it is: starts
// it (startLayer) if not Started; then runs rounds from
// LayerState.RoundsPublished+1 to MaxRounds through runRound; after each
// round but the last, if the layer has a moderator and moderatorDue,
// runModerator and applyDecision; ends the layer (endLayer) with
// EndRoundLimit when max_rounds is reached, EndCallLimit when the layer's
// budget is exhausted, or EndModeratorStop. It returns "" when the layer
// ended or a pause/cancel interrupted it, otherwise the reason the run
// must end: EndForumCallLimit and EndDeadline (incomplete),
// EndAttemptsExhausted, EndModeratorFailed, EndParticipantGone and
// EndHostError (failed).
func (c *Controller) runLayer(ctx context.Context, layer Layer) (EndReason, error) {
	ls := c.layerState(layer.ID)
	if !ls.Started {
		if err := c.startLayer(layer); err != nil {
			return "", err
		}
	}
	for round := ls.RoundsPublished + 1; round <= layer.MaxRounds; round++ {
		if c.interrupted() {
			return "", nil
		}
		reason, err := c.runRound(ctx, layer, round)
		if err != nil {
			return "", err
		}
		if reason == EndCallLimit {
			return "", c.endLayer(layer, reason)
		}
		if reason != "" || c.interrupted() {
			return reason, nil
		}
		if round == layer.MaxRounds || !moderatorDue(layer.Moderator, round) {
			continue
		}
		decision, reason, err := c.runModerator(ctx, layer, round)
		if err != nil {
			return "", err
		}
		if reason == EndCallLimit {
			return "", c.endLayer(layer, reason)
		}
		if reason != "" {
			return reason, nil
		}
		stop, err := c.applyDecision(layer, round, decision)
		if err != nil {
			return "", err
		}
		if stop {
			return "", c.endLayer(layer, EndModeratorStop)
		}
	}
	return "", c.endLayer(layer, EndRoundLimit)
}

// startLayer resolves and persists the layer's inputs (or reads an
// existing inputs.json back, after a crash between the write and the
// commit) and commits CommitLayerStarted.
func (c *Controller) startLayer(layer Layer) error {
	_, err := c.store.ReadLayerInputs(layer.ID)
	if errors.Is(err, ErrNotFound) {
		var inputs *LayerInputs
		inputs, err = c.router.Resolve(layer, c.produced())
		if err == nil {
			err = c.store.WriteLayerInputs(inputs)
		}
	}
	if err != nil {
		return fmt.Errorf("start layer %s: %w", layer.ID, err)
	}
	return c.commit(&Commit{Kind: CommitLayerStarted, Layer: layer.ID})
}

// endLayer commits CommitLayerEnded with the reason.
func (c *Controller) endLayer(layer Layer, reason EndReason) error {
	return c.commit(&Commit{Kind: CommitLayerEnded, Layer: layer.ID, Reason: reason})
}

// produced returns every layer's committed outputs, for the router.
func (c *Controller) produced() map[string][]OutputRecord {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make(map[string][]OutputRecord, len(c.state.Layers))
	for id, ls := range c.state.Layers {
		out[id] = append([]OutputRecord(nil), ls.Outputs...)
	}
	return out
}

// runRound runs one round of a layer per its delivery mode (§5). It
// returns the reason that stops the layer or the run, or "" to continue;
// "" with interrupted() true means a pause or cancel stopped it early.
func (c *Controller) runRound(ctx context.Context, layer Layer, round int) (EndReason, error) {
	switch layer.Delivery {
	case DeliveryAfterRound:
		return c.runRoundAfterRound(ctx, layer, round)
	case DeliveryPerTurn:
		return c.runRoundPerTurn(ctx, layer, round)
	}
	return "", fmt.Errorf("%w: layer %q has delivery %q", ErrCorrupt, layer.ID, layer.Delivery)
}

// runRoundAfterRound freezes cutoff = the current last commit seq, then
// dispatches every turn of the round that has no committed output
// (runTurn) with at most limits.max_parallel_calls in flight, each seeing
// only commits up to cutoff. When every turn is committed it appends
// CommitRoundPublished and writes the round's outputs to the transcript
// in participant order (appendTurnTranscript). The first failing turn's
// reason stops the round once the in-flight turns return; turns already
// committed stay committed. A pause request stops new dispatch and lets
// the in-flight turns finish.
func (c *Controller) runRoundAfterRound(ctx context.Context, layer Layer, round int) (EndReason, error) {
	return "", errNotImplemented
}

// runRoundPerTurn dispatches each turn in participant order with cutoff =
// the current last commit seq, commits it and writes it to the transcript
// before sending the next (§5). A turn that is already committed is
// skipped, which is how a resume lands on the first unfinished turn.
func (c *Controller) runRoundPerTurn(ctx context.Context, layer Layer, round int) (EndReason, error) {
	for _, pid := range layer.Participants {
		if c.interrupted() {
			return "", nil
		}
		if c.committedOutput(layer.ID, TurnID(round, pid)) != nil {
			continue
		}
		out, reason, err := c.runTurn(ctx, layer, round, pid, c.State().Seq)
		if err != nil || reason != "" {
			return reason, err
		}
		if err := c.appendTurnTranscript(layer, out); err != nil {
			return "", err
		}
	}
	return "", nil
}

// committedOutput returns the output committed for a turn ID, or nil.
func (c *Controller) committedOutput(layerID, turn string) *OutputRecord {
	c.mu.Lock()
	defer c.mu.Unlock()
	ls := c.state.Layers[layerID]
	if ls == nil {
		return nil
	}
	for i := range ls.Outputs {
		if ls.Outputs[i].Turn == turn {
			return &ls.Outputs[i]
		}
	}
	return nil
}

// checkLimits reports the reason the forum (EndForumCallLimit, EndDeadline)
// or the layer (EndCallLimit) can no longer dispatch, counting the calls
// a dispatch is about to add. "" means a call may be sent.
func (c *Controller) checkLimits(layer Layer, pending int, now time.Time) EndReason {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !now.Before(c.snap.Deadline) {
		return EndDeadline
	}
	if c.state.Calls+pending > c.cfg.Limits.MaxCalls {
		return EndForumCallLimit
	}
	if ls := c.state.Layers[layer.ID]; layer.MaxCalls > 0 && ls != nil && ls.Calls+pending > layer.MaxCalls {
		return EndCallLimit
	}
	return ""
}

// interrupted reports whether a pause or cancel was requested; the caller
// stops dispatching and lets Run finish the transition.
func (c *Controller) interrupted() bool {
	return c.pause.Load() || c.cancel.Load()
}

// wait returns the Ask wait: limits.call_timeout_seconds bounded by the
// time left to the run deadline (never negative).
func (c *Controller) wait(now time.Time) time.Duration {
	timeout := time.Duration(c.cfg.Limits.CallTimeoutSeconds) * time.Second
	if left := c.snap.Deadline.Sub(now); left < timeout {
		timeout = left
	}
	if timeout < 0 {
		return 0
	}
	return timeout
}

// commit appends commit to the log, applies it to the in-memory state
// exactly as Replay would (applyCommit), and rewrites state.json. It is
// the only way state changes.
func (c *Controller) commit(commit *Commit) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	seq, err := c.store.AppendCommit(commit)
	if err != nil {
		return fmt.Errorf("commit %s: %w", commit.Kind, err)
	}
	commit.Seq = seq
	if err := applyCommit(c.state, commit); err != nil {
		return err
	}
	return c.store.WriteState(c.state)
}

// applyCommit folds one commit into st. Replay is a loop over applyCommit
// plus the attempt-derived counters, so the two can never disagree.
func applyCommit(st *State, commit *Commit) error {
	return errNotImplemented
}

// end commits CommitEnded with the terminal status and reason, builds the
// Result from the result layers and writes result.json. Deleting temporary
// agents and notifying the launcher are the service's job, after this
// returns (§9 Completion).
func (c *Controller) end(status Status, reason EndReason) error {
	if err := c.commit(&Commit{Kind: CommitEnded, Status: status, Reason: reason}); err != nil {
		return err
	}
	st := c.State()
	return c.store.WriteResult(resultOf(c.cfg, c.snap, &st))
}
