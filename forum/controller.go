// ClawEh
// License: MIT
//
// Copyright (c) 2026 Tenebris Technologies Inc.

package forum

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"sync/atomic"
	"time"
)

// The controller. A forumController
// executes one forum from its on-disk state. It is built only by openForum
// (recover.go), whether the forum was launched a moment ago or is being
// resumed after a restart: there is one code path, driven by what the
// store already holds.
//
// Run is idempotent over the store. It walks the enabled layers in
// snapshot order, skips every layer, round and turn that already has a
// commit, and performs the first action that does not. A turn whose
// latest attempt has no reply is resent. Nothing is kept in memory
// that is not also on disk before the next dispatch.

// forumController runs one forum.
type forumController struct {
	store *forumStore
	cfg   *Config
	snap  *Snapshot
	parts *Participants
	host  Host
	// ref names the forum in refusals (storeRef: the name in its current
	// configuration, which cannot change while a controller is open), and
	// logName in log lines ("forum <ref> run <n>").
	ref     string
	logName string
	// schemas are the named schemas; decisionSchemas the effective
	// moderator schema per layer (Snapshot.ModeratorSchemas), both
	// compiled at openForum.
	schemas         map[string]*compiledSchema
	decisionSchemas map[string]*compiledSchema
	router          *router

	// mu guards state, commits, attempts and cancelActive. Every state
	// mutation goes through commitWhen, every attempts mutation through
	// reserve and dispatch.
	mu    sync.Mutex
	state *State
	// commits is the commit log as applied, the source of the visibility
	// rules (eligible events, directed messages, what a participant was
	// already sent).
	commits []Commit
	// attempts caches forumStore.ListAttempts per layer at openForum and is updated
	// as requests and replies are written.
	attempts map[string][]AttemptRecord
	// gone lists the created participants openForum found missing; Run
	// ends the forum failed when it is non-empty.
	gone []string

	// dispatchMu serialises reservations, so the limit check and the
	// CommitAttempt that consumes the budget are one step even when an
	// after_round round has several turns in flight.
	dispatchMu sync.Mutex

	// exited is true (under mu) from the moment Run returns until it is
	// called again. Requests are refused while it is set: a request that
	// lands after Run has exited could otherwise leave the forum pausing or
	// cancelling with nothing to complete the transition.
	exited bool

	// pause and cancel are set (under mu, right after the request is
	// committed) by RequestPause/RequestCancel and polled between
	// dispatches; cancel also cancels in-flight asks through cancelActive.
	pause        atomic.Bool
	cancel       atomic.Bool
	cancelActive context.CancelFunc

	// clock measures holds, call timeouts and the run deadline
	// (Host.Clock, else the system clock). cooldownPoll is how often a
	// held turn looks again and releaseDelay draws its release delay
	// (awaitModel); tests shorten or fix them.
	clock        Clock
	cooldownPoll time.Duration
	releaseDelay func() time.Duration

	// tmu guards transcriptSeq: the last publication commit already in
	// transcript.md.
	tmu           sync.Mutex
	transcriptSeq int

	// crashHook, when set (tests only), is called after every durable
	// write with the write's name; returning true simulates the process
	// dying at that point: the write stands, and every later write of
	// this controller fails with errCrashed.
	crashHook func(event string) bool
	dead      atomic.Bool
}

// errSkip is returned by a commitWhen guard to leave the log unchanged
// without an error (an idempotent request, a transition already made).
var errSkip = errors.New("forum: commit not needed")

// errCrashed is what every write returns after crashHook fired.
var errCrashed = errors.New("forum: simulated crash")

// errRunEnded is wrapped by RequestPause and RequestCancel when Run has
// already returned: the caller waits for the run to be released and takes
// the forum over instead.
var errRunEnded = errors.New("the forum's run has stopped")

// Run drives the forum until it is terminal or paused and returns the
// resulting status. Forum-level failures (exhausted attempts, a gone
// participant, limits) become a terminal state and a nil error. A non-nil
// error means the store refused a write or ctx ended (the host is
// shutting down); the forum is then left as it is on disk, never ended
// as failed, and a later openForum resumes it. Once a cancel is requested it
// dominates: the run ends cancelled whatever else was pending.
//
// Sequence: return a
// terminal status at once (writing result.json if a crash lost it);
// finish an interrupted transition (StatusPausing -> paused,
// StatusCancelling -> cancelled); commit CommitLaunched for a queued
// forum; refuse a paused one (ErrInvalidState: resuming is the caller's
// decision, recorded as CommitResumed before Run); then for each enabled layer
// not Ended, runLayer; then end with StatusCompleted. A pause or cancel
// requested while a layer runs is honoured between dispatches.
//
// A run that meets ErrCorrupt (a record that no longer verifies, a commit
// the log refuses) ends the forum failed with EndCorrupt, logged at Error
// with the cause, rather than leaving it to fail the same way at every
// start. While Run is not executing after having returned, RequestPause
// and RequestCancel are refused (errRunEnded); the caller that started
// Run re-reads State afterwards and calls Run again while it is pausing
// or cancelling, so no accepted request is ever left unfinished.
func (c *forumController) Run(ctx context.Context) (Status, error) {
	c.mu.Lock()
	c.exited = false
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		c.exited = true
		c.mu.Unlock()
	}()
	status, err := c.run(ctx)
	if err != nil && errors.Is(err, ErrCorrupt) && !c.dead.Load() {
		c.host.Logger.Errorf("%s: its records are corrupt; ending it failed: %v", c.logName, err)
		return c.end(StatusFailed, EndCorrupt)
	}
	return status, err
}

// run is Run's body.
func (c *forumController) run(ctx context.Context) (Status, error) {
	runCtx, cancelRun := context.WithCancel(ctx)
	defer cancelRun()
	c.mu.Lock()
	c.cancelActive = cancelRun
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		c.cancelActive = nil
		c.mu.Unlock()
	}()
	if c.cancel.Load() {
		cancelRun()
	}
	switch st := c.State(); st.Status {
	case StatusCompleted, StatusIncomplete, StatusFailed, StatusCancelled:
		return st.Status, c.ensureResult()
	case StatusPausing, StatusCancelling:
		return c.settle()
	case StatusQueued:
		if err := c.commitWhen(statusIs(StatusQueued), &Commit{Kind: CommitLaunched}, nil); err != nil && !errors.Is(err, errSkip) {
			return "", err
		}
	case StatusPaused:
		return StatusPaused, fmt.Errorf("run forum %s: %w: it is paused; commit %s before running it", c.snap.ForumID, ErrInvalidState, CommitResumed)
	case StatusNew, StatusRunning: // a run is never new
	}
	if c.interrupted() {
		return c.settle()
	}
	if len(c.gone) > 0 {
		c.host.Logger.Errorf("%s: temporary participants no longer exist: %v", c.logName, c.gone)
		return c.end(StatusFailed, EndParticipantGone)
	}
	for _, id := range c.snap.Layers {
		layer, ok := c.cfg.Layer(id)
		if !ok {
			return "", fmt.Errorf("%w: snapshot layer %q is not configured", ErrCorrupt, id)
		}
		if c.layerState(id).Ended {
			continue
		}
		reason, err := c.runLayer(runCtx, layer)
		if err != nil {
			return "", err
		}
		if c.interrupted() {
			return c.settle()
		}
		switch reason {
		case EndForumCallLimit, EndDeadline:
			return c.end(StatusIncomplete, reason)
		case EndAttemptsExhausted, EndModeratorFailed, EndParticipantGone, EndHostError, EndCorrupt:
			return c.end(StatusFailed, reason)
		case EndCompleted, EndRoundLimit, EndCallLimit, EndModeratorStop, EndCancelled:
		}
	}
	return c.end(StatusCompleted, EndCompleted)
}

// RequestPause asks the run to stop dispatching: it commits
// CommitPauseRequested (status pausing); in-flight asks finish within
// their timeouts and the run then commits CommitPaused and returns. A
// forum already pausing or paused is left as it is (idempotent). It is
// ErrInvalidState, naming the forum, in any other state but running:
// cancellation dominates a pause, and a terminal forum stays terminal.
// After Run has returned it is refused (errRunEnded, see Run).
func (c *forumController) RequestPause() error {
	err := c.commitWhen(func(st *State) error {
		if c.exited {
			return runEnded(c.ref, "paused")
		}
		switch st.Status {
		case StatusRunning:
			return nil
		case StatusPausing, StatusPaused:
			return errSkip
		case StatusCancelling:
			return invalidState("forum %s is being cancelled and cannot be paused", c.ref)
		case StatusNew, StatusQueued, StatusCompleted, StatusIncomplete, StatusFailed, StatusCancelled:
		}
		return invalidState("forum %s is %s and cannot be paused", c.ref, st.Status)
	}, &Commit{Kind: CommitPauseRequested}, func() { c.pause.Store(true) })
	if errors.Is(err, errSkip) {
		return nil
	}
	return err
}

// RequestCancel commits CommitCancelRequested (status cancelling) and
// cancels in-flight asks; the run then ends with StatusCancelled, keeping
// every committed output. Repeating it is harmless. It is ErrInvalidState,
// naming the forum, when the status is terminal. After Run has returned
// it is refused (errRunEnded, see Run).
func (c *forumController) RequestCancel() error {
	err := c.commitWhen(func(st *State) error {
		switch {
		case c.exited:
			return runEnded(c.ref, "cancelled")
		case st.Status.Terminal():
			return invalidState("forum %s is already %s", c.ref, st.Status)
		case st.Status == StatusCancelling:
			return errSkip
		}
		return nil
	}, &Commit{Kind: CommitCancelRequested}, func() { c.cancel.Store(true) })
	if err != nil && !errors.Is(err, errSkip) {
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

// runEnded is the refusal of a request made after Run returned; ref names
// the forum (Ref).
func runEnded(ref, verb string) error {
	return &stateError{msg: fmt.Sprintf("forum %s has stopped running and cannot be %s by this run", ref, verb), cause: errRunEnded}
}

// State returns a deep copy of the current derived state.
func (c *forumController) State() State {
	c.mu.Lock()
	defer c.mu.Unlock()
	return cloneState(c.state)
}

// Snapshot returns the launch snapshot.
func (c *forumController) Snapshot() *Snapshot { return c.snap }

// Config returns the configuration as launched.
func (c *forumController) Config() *Config { return c.cfg }

// Participants returns participants.json as loaded.
func (c *forumController) Participants() *Participants { return c.parts }

// cloneState copies st so the caller can read it without the lock.
func cloneState(st *State) State {
	out := *st
	out.Layers = make(map[string]*LayerState, len(st.Layers))
	for id, ls := range st.Layers {
		cp := *ls
		cp.Outputs = slices.Clone(ls.Outputs)
		cp.Decisions = slices.Clone(ls.Decisions)
		out.Layers[id] = &cp
	}
	return out
}

// settle completes a requested transition once nothing is in flight: a
// cancelling forum ends cancelled, a pausing one commits CommitPaused. The
// status is re-read under the commit lock, so a cancel that lands while a
// pause is being completed still wins.
func (c *forumController) settle() (Status, error) {
	for {
		switch st := c.State().Status; st {
		case StatusCancelling:
			return c.end(StatusCancelled, EndCancelled)
		case StatusPausing:
			err := c.commitWhen(statusIs(StatusPausing), &Commit{Kind: CommitPaused}, nil)
			if errors.Is(err, errSkip) {
				continue // the status changed under us; look again
			}
			if err != nil {
				return "", err
			}
			c.host.Logger.Infof("%s: paused", c.logName)
			return StatusPaused, nil
		case StatusCompleted, StatusIncomplete, StatusFailed, StatusCancelled, StatusPaused:
			return st, nil
		case StatusNew, StatusQueued, StatusRunning:
			// A flag without its commit cannot happen (both are set under
			// the commit lock); treat it as a pause request that lost its
			// record and stop without changing the status.
			return st, nil
		}
	}
}

// statusIs is a commitWhen guard: the commit lands only in that status.
func statusIs(want Status) func(*State) error {
	return func(st *State) error {
		if st.Status != want {
			return errSkip
		}
		return nil
	}
}

// layerState returns a copy of a layer's state (zero when none yet).
func (c *forumController) layerState(layerID string) LayerState {
	c.mu.Lock()
	defer c.mu.Unlock()
	if ls := c.state.Layers[layerID]; ls != nil {
		cp := *ls
		cp.Outputs = slices.Clone(ls.Outputs)
		cp.Decisions = slices.Clone(ls.Decisions)
		return cp
	}
	return LayerState{}
}

// runLayer executes one layer from wherever its state says it is: starts
// it (startLayer) if not Started; then for each round, runs it (runRound)
// unless already published and, after every round but the last for which
// moderatorDue holds, makes sure the moderator's decision is committed
// (runModerator, applyDecision) and acts on it. It ends the layer
// (endLayer) with EndRoundLimit when max_rounds is reached, EndCallLimit
// when the layer's budget is exhausted, or EndModeratorStop. It returns
// "" when the layer ended or a pause/cancel interrupted it, otherwise the
// reason the run must end: EndForumCallLimit and EndDeadline
// (incomplete), EndAttemptsExhausted, EndModeratorFailed,
// EndParticipantGone and EndHostError (failed).
//
// Walking every round from 1 (and skipping what is done) is what makes a
// resume land on the first unfinished action, including a moderator check
// after a round that was published just before a crash.
func (c *forumController) runLayer(ctx context.Context, layer Layer) (EndReason, error) {
	if !c.layerState(layer.ID).Started {
		if err := c.startLayer(layer); err != nil {
			return "", err
		}
	}
	for round := 1; round <= layer.MaxRounds; round++ {
		if round > c.layerState(layer.ID).RoundsPublished {
			if c.interrupted() {
				return "", nil
			}
			reason, err := c.runRound(ctx, layer, round)
			switch {
			case err != nil:
				return "", err
			case reason == EndCallLimit:
				return "", c.endLayer(layer, reason)
			case reason != "":
				return reason, nil
			case c.layerState(layer.ID).RoundsPublished < round:
				return "", nil // interrupted mid-round
			}
		}
		if round == layer.MaxRounds || !moderatorDue(layer.Moderator, round) {
			continue
		}
		d := c.committedDecision(layer.ID, round)
		if d == nil {
			if c.interrupted() {
				return "", nil
			}
			decision, reason, err := c.runModerator(ctx, layer, round)
			switch {
			case err != nil:
				return "", err
			case reason == EndCallLimit:
				return "", c.endLayer(layer, reason)
			case reason != "":
				return reason, nil
			case decision == nil:
				return "", nil // interrupted
			}
			if err := c.applyDecision(layer, round, decision); err != nil {
				return "", err
			}
			if d = c.committedDecision(layer.ID, round); d == nil {
				return "", nil // cancelled before the decision could be committed
			}
		}
		if d.Decision == DecisionStop {
			return "", c.endLayer(layer, EndModeratorStop)
		}
	}
	return "", c.endLayer(layer, EndRoundLimit)
}

// startLayer resolves and persists the layer's inputs (or reads an
// existing inputs.json back, after a crash between the write and the
// commit) and commits CommitLayerStarted.
func (c *forumController) startLayer(layer Layer) error {
	_, err := c.store.ReadLayerInputs(layer.ID)
	if errors.Is(err, ErrNotFound) {
		var inputs *LayerInputs
		inputs, err = c.router.Resolve(layer, c.produced())
		if err == nil {
			err = c.durable("inputs", func() error { return c.store.WriteLayerInputs(inputs) })
		}
	}
	if err != nil {
		return fmt.Errorf("start layer %s: %w", layer.ID, err)
	}
	c.host.Logger.Infof("%s: layer %s started", c.logName, layer.ID)
	return c.commit(&Commit{Kind: CommitLayerStarted, Layer: layer.ID})
}

// endLayer commits CommitLayerEnded with the reason.
func (c *forumController) endLayer(layer Layer, reason EndReason) error {
	c.host.Logger.Infof("%s: layer %s ended: %s", c.logName, layer.ID, reason)
	return c.commit(&Commit{Kind: CommitLayerEnded, Layer: layer.ID, Reason: reason})
}

// produced returns every layer's published outputs, for the router. An
// after_round round that never reached its publication (a layer ended by
// its call budget mid-round) stays hidden: its outputs are committed but
// never routed.
func (c *forumController) produced() map[string][]OutputRecord {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make(map[string][]OutputRecord, len(c.state.Layers))
	for id, ls := range c.state.Layers {
		layer, _ := c.cfg.Layer(id)
		out[id] = publishedOutputs(layer, ls)
	}
	return out
}

// publishedOutputs returns a layer's outputs that were published: all of
// them for per_turn, those of published rounds for after_round, ordered by
// round and then the layer's participant order (never by commit order,
// which concurrent turns make timing-dependent).
func publishedOutputs(layer Layer, ls *LayerState) []OutputRecord {
	out := make([]OutputRecord, 0, len(ls.Outputs))
	for _, o := range ls.Outputs {
		if layer.Delivery == DeliveryPerTurn || o.Round <= ls.RoundsPublished {
			out = append(out, o)
		}
	}
	return orderOutputs(layer, out)
}

// runRound runs one round of a layer per its delivery mode. It
// returns the reason that stops the layer or the run, or "" to continue;
// "" with the round unpublished means a pause or cancel stopped it early.
func (c *forumController) runRound(ctx context.Context, layer Layer, round int) (EndReason, error) {
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
// in participant order. A failing turn stops new dispatch; once the
// in-flight turns return, the reason of the first failing turn in
// participant order is returned (so the outcome does not depend on
// timing). Turns already committed stay committed. A pause request stops
// new dispatch and lets the in-flight turns finish.
func (c *forumController) runRoundAfterRound(ctx context.Context, layer Layer, round int) (EndReason, error) {
	cutoff := c.State().Seq
	type result struct {
		reason EndReason
		err    error
	}
	results := make([]result, len(layer.Participants))
	slots := make(chan struct{}, max(1, c.snap.Limits.MaxParallelCalls))
	var (
		wg      sync.WaitGroup
		stopped atomic.Bool
	)
	for i, pid := range layer.Participants {
		if c.committedOutput(layer.ID, turnID(round, pid)) != nil {
			continue
		}
		slots <- struct{}{}
		if stopped.Load() || c.interrupted() || ctx.Err() != nil {
			<-slots
			break
		}
		wg.Go(func() {
			defer func() { <-slots }()
			_, reason, err := c.runTurn(ctx, layer, round, pid, cutoff)
			results[i] = result{reason, err}
			if reason != "" || err != nil {
				stopped.Store(true)
			}
		})
	}
	wg.Wait()
	for _, r := range results {
		if r.err != nil {
			return "", r.err
		}
	}
	for _, r := range results {
		if r.reason != "" {
			return r.reason, nil
		}
	}
	if err := ctx.Err(); err != nil && !c.cancel.Load() {
		return "", err
	}
	for _, pid := range layer.Participants {
		if c.committedOutput(layer.ID, turnID(round, pid)) == nil {
			return "", nil // interrupted
		}
	}
	commit := &Commit{Kind: CommitRoundPublished, Layer: layer.ID, Round: round}
	if err := c.commitWhen(notCancelling, commit, nil); err != nil {
		if errors.Is(err, errSkip) {
			return "", nil
		}
		return "", err
	}
	return "", c.publishTranscript(commit)
}

// runRoundPerTurn dispatches each turn in participant order with cutoff =
// the current last commit seq, commits it and writes it to the transcript
// before sending the next. A turn that is already committed is
// skipped, which is how a resume lands on the first unfinished turn.
func (c *forumController) runRoundPerTurn(ctx context.Context, layer Layer, round int) (EndReason, error) {
	for _, pid := range layer.Participants {
		if c.interrupted() {
			return "", nil
		}
		if err := ctx.Err(); err != nil {
			return "", err
		}
		if c.committedOutput(layer.ID, turnID(round, pid)) != nil {
			continue
		}
		out, reason, err := c.runTurn(ctx, layer, round, pid, c.State().Seq)
		if err != nil || reason != "" {
			return reason, err
		}
		if out == nil {
			return "", nil // interrupted
		}
	}
	return "", nil
}

// committedOutput returns the output committed for a turn ID, or nil.
func (c *forumController) committedOutput(layerID, turn string) *OutputRecord {
	c.mu.Lock()
	defer c.mu.Unlock()
	ls := c.state.Layers[layerID]
	if ls == nil {
		return nil
	}
	for i := range ls.Outputs {
		if ls.Outputs[i].Turn == turn {
			out := ls.Outputs[i]
			return &out
		}
	}
	return nil
}

// checkLimits reports the reason the forum (EndDeadline,
// EndForumCallLimit) or the layer (EndCallLimit) can no longer dispatch,
// counting the calls a dispatch is about to add, in that order of
// precedence. "" means a call may be sent. Limits come from the snapshot,
// never from a re-read configuration.
func (c *forumController) checkLimits(layer Layer, pending int, now time.Time) EndReason {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !now.Before(c.snap.Deadline) {
		return EndDeadline
	}
	if c.state.Calls+pending > c.snap.Limits.MaxCalls {
		return EndForumCallLimit
	}
	if ls := c.state.Layers[layer.ID]; layer.MaxCalls > 0 && ls != nil && ls.Calls+pending > layer.MaxCalls {
		return EndCallLimit
	}
	return ""
}

// interrupted reports whether a pause or cancel was requested; the caller
// stops dispatching and lets Run finish the transition.
func (c *forumController) interrupted() bool {
	return c.pause.Load() || c.cancel.Load()
}

// deadlinePassed reports whether the run deadline has been reached at now.
func (c *forumController) deadlinePassed(now time.Time) bool {
	return !now.Before(c.snap.Deadline)
}

// wait returns the Ask wait: limits.call_timeout_seconds bounded by the
// time left to the run deadline (never negative).
func (c *forumController) wait(now time.Time) time.Duration {
	timeout := time.Duration(c.snap.Limits.CallTimeoutSeconds) * time.Second
	if left := c.snap.Deadline.Sub(now); left < timeout {
		timeout = left
	}
	if timeout < 0 {
		return 0
	}
	return timeout
}

// commit appends commit to the log unconditionally (see commitWhen).
func (c *forumController) commit(commit *Commit) error {
	return c.commitWhen(nil, commit, nil)
}

// commitWhen folds commit into a copy of the state with replayApply (the
// same fold replay uses, so the live controller and a restart can never
// disagree) and, only if that succeeds, appends it to the log, swaps the
// copy in, records the commit in the in-memory log and rewrites
// state.json. A commit replay would reject never reaches the log. guard, when set, runs first under the lock and can
// refuse the commit (errSkip for "not needed", or an error); after, when
// set, runs under the lock once the commit is applied. It is the only way
// state changes.
func (c *forumController) commitWhen(guard func(*State) error, commit *Commit, after func()) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.dead.Load() {
		return errCrashed
	}
	if guard != nil {
		if err := guard(c.state); err != nil {
			return err
		}
	}
	// Fold first, into a copy: a commit replay would reject never reaches
	// the log. Seq and At are the values AppendCommit is about to assign.
	next := cloneState(c.state)
	trial := *commit
	trial.Seq, trial.At = c.state.Seq+1, time.Now().UTC().Round(0)
	if err := replayApply(c.cfg, c.snap, &next, &trial); err != nil {
		return fmt.Errorf("commit %s refused: %w", commit.Kind, err)
	}
	if err := c.store.AppendCommit(trial.Seq, commit); err != nil {
		return fmt.Errorf("commit %s: %w", commit.Kind, err)
	}
	next.UpdatedAt = commit.At
	c.state = &next
	c.commits = append(c.commits, *commit)
	if after != nil {
		after()
	}
	if c.crashed("commit-before-state") {
		return errCrashed
	}
	if err := c.store.WriteState(c.state); err != nil {
		return fmt.Errorf("commit %s: %w", commit.Kind, err)
	}
	if c.crashed("commit") {
		return errCrashed
	}
	return nil
}

// notCancelling is a commitWhen guard for publications and outputs: once
// a cancel is requested nothing more is published (late replies stay on
// disk for audit only).
func notCancelling(st *State) error {
	if st.Status == StatusCancelling || st.Status.Terminal() {
		return errSkip
	}
	return nil
}

// durable runs one store write (outside the commit log) and then gives
// the crash hook its chance; after a simulated crash it refuses to write.
func (c *forumController) durable(event string, write func() error) error {
	if c.dead.Load() {
		return errCrashed
	}
	if err := write(); err != nil {
		return err
	}
	if c.crashed(event) {
		return errCrashed
	}
	return nil
}

// crashed reports whether crashHook simulates a crash after event, and
// marks the controller dead when it does.
func (c *forumController) crashed(event string) bool {
	if c.crashHook == nil || !c.crashHook(event) {
		return false
	}
	c.dead.Store(true)
	return true
}

// end commits CommitEnded with the terminal status and reason, then
// writes result.json (ensureResult). Once a cancel is requested the run
// ends cancelled whatever the reason (cancellation dominates). A forum
// already terminal is left as it is. Deleting temporary agents and
// notifying the launcher are the service's job, after this returns.
func (c *forumController) end(status Status, reason EndReason) (Status, error) {
	commit := &Commit{Kind: CommitEnded, Status: status, Reason: reason}
	err := c.commitWhen(func(st *State) error {
		if st.Status.Terminal() {
			return errSkip
		}
		if st.Status == StatusCancelling {
			commit.Status, commit.Reason = StatusCancelled, EndCancelled
		}
		return nil
	}, commit, nil)
	if err != nil && !errors.Is(err, errSkip) {
		return "", err
	}
	st := c.State()
	c.host.Logger.Infof("%s: ended %s (%s) after %d calls", c.logName, st.Status, st.Reason, st.Calls)
	return st.Status, c.ensureResult()
}

// ensureResult writes result.json for a terminal forum unless it exists
// (a crash between CommitEnded and the write leaves it missing).
func (c *forumController) ensureResult() error {
	_, err := c.store.ReadResult()
	if err == nil || !errors.Is(err, ErrNotFound) {
		return err
	}
	st := c.State()
	res := buildResult(c.cfg, c.snap, &st)
	return c.durable("result", func() error { return c.store.WriteResult(res) })
}

// buildResult builds the Result manifest from the loaded records, the one
// builder of result.json and of every partial manifest (Complete false, no
// EndedAt, while the status is not terminal):
//
//   - Layers: the result layers in snapshot order with their published
//     outputs only, so an after_round round that was not published never
//     shows;
//   - Omissions (DESIGN.md §8.9): a result layer that did not run or did
//     not end, a turn of a started round with no committed output, and an
//     after_round round whose outputs were committed but never published;
//   - OtherLayers, only when the result layers have no output at all: the
//     other enabled layers' outputs, the published ones while the run
//     goes on and every committed one once it has ended, so the launcher
//     can reach the work of a run that failed early.
func buildResult(cfg *Config, snap *Snapshot, st *State) *Result {
	res := &Result{
		ForumID:    snap.ForumID,
		Run:        snap.Run,
		Name:       snap.Name,
		Status:     st.Status,
		Reason:     st.Reason,
		LaunchedAt: snap.LaunchedAt,
		Complete:   st.Status == StatusCompleted,
		Calls:      st.Calls,
		Layers:     []LayerResult{},
		Transcript: fileTranscript,
		// Every layer's, as forum_status counts them.
		ResentAfterRestart: resentAfterRestart(st),
	}
	if st.Status.Terminal() {
		res.EndedAt = st.UpdatedAt
	}
	for _, id := range snap.ResultLayers {
		layer, _ := cfg.Layer(id)
		ls := st.Layers[id]
		if ls == nil {
			ls = &LayerState{}
		}
		res.Layers = append(res.Layers, LayerResult{
			LayerID: id, Ended: ls.Ended, EndReason: ls.EndReason, Outputs: publishedOutputs(layer, ls),
		})
		res.Omissions = append(res.Omissions, layerOmissions(layer, ls)...)
	}
	for _, l := range res.Layers {
		if len(l.Outputs) > 0 {
			return res
		}
	}
	for _, id := range snap.Layers {
		if slices.Contains(snap.ResultLayers, id) {
			continue
		}
		layer, _ := cfg.Layer(id)
		ls := st.Layers[id]
		if ls == nil {
			continue
		}
		outs := publishedOutputs(layer, ls)
		if st.Status.Terminal() {
			outs = orderOutputs(layer, slices.Clone(ls.Outputs))
		}
		if len(outs) > 0 {
			res.OtherLayers = append(res.OtherLayers, LayerResult{LayerID: id, Ended: ls.Ended, EndReason: ls.EndReason, Outputs: outs})
		}
	}
	return res
}

// layerOmissions lists what one result layer lacks (see buildResult).
func layerOmissions(layer Layer, ls *LayerState) []string {
	if !ls.Started {
		return []string{fmt.Sprintf("layer %s did not run", layer.ID)}
	}
	var out []string
	if !ls.Ended {
		out = append(out, fmt.Sprintf("layer %s did not end", layer.ID))
	}
	lastRound := max(ls.Round, ls.RoundsPublished)
	for round := 1; round <= lastRound; round++ {
		committed := 0
		for _, pid := range layer.Participants {
			if !slices.ContainsFunc(ls.Outputs, func(o OutputRecord) bool { return o.Round == round && o.ParticipantID == pid }) {
				out = append(out, fmt.Sprintf("layer %s round %d: no output from %s", layer.ID, round, pid))
				continue
			}
			committed++
		}
		if layer.Delivery == DeliveryAfterRound && round > ls.RoundsPublished && committed > 0 {
			out = append(out, fmt.Sprintf("layer %s round %d: %d committed output(s) not published (round incomplete)", layer.ID, round, committed))
		}
	}
	return out
}
