// ClawEh
// License: MIT
//
// Copyright (c) 2026 Tenebris Technologies Inc.

package classifier

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/PivotLLM/ClawEh/classifier/classifierfs"
)

// itemMode says what handling one item may write.
type itemMode int

const (
	modeLive  itemMode = iota // processed table; the action runs
	modeDry                   // dry_run table only; no action
	modeTest                  // nothing stored, no action (classifier_test)
	modeRerun                 // like live (or dry when the definition is), reclassified on purpose
)

// decision is the outcome of deciding one item: the Decision, the item's
// data (fields for the activity log, fetched values for the action), and a
// run-wide error (no usable model, cooldown) that ends the run. system is
// the system prompt of the first model request, "" when none was made.
// cause is what a failure is down to. interrupted is a failure because the
// service stopped (its context was cancelled): nothing about it is stored
// or counted, so the next run takes the item again.
type decision struct {
	Decision
	data        itemData
	fatal       error
	system      string
	cause       cause
	interrupted bool
}

// decide fetches one item and classifies it. reuse, when it holds a
// category the definition still has, is the first attempt's answer (design
// 7.1b): no model call is made.
func (s *Service) decide(ctx context.Context, scope Scope, def *Definition, in instructions, item listedItem, reuse *processedRow) decision {
	var d decision
	d.ItemID = item.id
	if f := def.Fetch; f != nil {
		args, err := substitute(f.Args, item.value, nil, "")
		if err != nil {
			return d.fetchFailed(item, fmt.Errorf("fetch.args: %w", err))
		}
		result, err := s.host.Tools.Call(ctx, scope.AgentID, f.Tool, nonNilArgs(args))
		if err != nil {
			return d.fetchFailed(item, &toolError{role: roleFetch, tool: f.Tool, err: err})
		}
		d.data = fieldsFromFetch(result, f)
		d.MissingFields = d.data.missing
		if len(d.data.fields) == 0 {
			// Nothing to classify: the item is not decided blind.
			return d.fetchFailed(item, noFetchFields(f.Tool, d.data, result))
		}
	} else {
		d.data = fieldsFromElement(item.value)
	}
	d.data.fields = truncate(d.data.fields, def.itemBudget(s.host.ceilings()))
	d.ShortenedFields = d.data.shortened()

	if reuse != nil && reuse.category != "" {
		if c, ok := def.categoryNamed(reuse.category); ok {
			d.Category, d.Reason, d.Model = c.Name, reuse.reason, reuse.model
			return d
		}
	}

	delim := newDelimiter()
	system, user := buildPrompt(def, in, d.data, "", delim)
	d.system = system
	reply, model, err := s.host.Model.Complete(ctx, scope.AgentID, system, user)
	if err != nil {
		return d.modelFailed(model, err)
	}
	d.Model = model
	category, reason, ok := parseReply(reply, def)
	if !ok {
		first := reply
		system, user = buildPrompt(def, in, d.data, reply, newDelimiter())
		reply, model, err = s.host.Model.Complete(ctx, scope.AgentID, system, user)
		if err != nil {
			return d.modelFailed(model, err)
		}
		d.Model = model
		if category, reason, ok = parseReply(reply, def); !ok {
			d.Outcome = OutcomeUnclassified
			d.Replies = []string{cutRunes(first, ActivityValueMax), cutRunes(reply, ActivityValueMax)}
			return d
		}
	}
	d.Category, d.Reason = category, reason
	return d
}

// noFetchFields is the failure of a fetch result in which no fetch field
// resolved, naming the missing fields (and, when the result is not JSON,
// the parse error and what came back, as the detail).
func noFetchFields(tool string, data itemData, result string) error {
	e := &noFieldsError{tool: tool, missing: data.missing}
	if data.notJSON != nil {
		e.notJSON = &notJSONError{role: roleFetch, tool: tool, parseErr: data.notJSON, result: result}
	}
	return e
}

// redactCause is c with its texts redacted (Host.Redact).
func (s *Service) redactCause(c cause) cause {
	c.summary, c.detail = s.host.redact(c.summary), s.host.redact(c.detail)
	return c
}

func (d decision) failed(err error) decision {
	d.Outcome, d.Error, d.cause = OutcomeFailed, err.Error(), causeOf(err)
	return d
}

// fetchFailed is an item whose fetch failed: its activity row shows the
// list entry's own fields instead, so the item can be recognised.
func (d decision) fetchFailed(item listedItem, err error) decision {
	d.data = fieldsFromElement(item.value)
	return d.failed(err)
}

// modelFailed is a model call of model that failed: the item fails; no
// usable model and a cooldown also end the run (fatal), so the other items
// are not each sent to fail.
func (d decision) modelFailed(model string, err error) decision {
	if _, cooling := errors.AsType[*CooldownError](err); cooling || errors.Is(err, ErrNoModel) {
		d.fatal = err
		return d.failed(err)
	}
	return d.failed(&modelError{model: model, err: err})
}

// act runs the action for the decision's category (design 6): none is
// recorded only; a substitution or tool error fails the item, and a tool
// error records the arguments it was called with.
func (s *Service) act(ctx context.Context, scope Scope, def *Definition, item listedItem, d *decision) {
	action, ok := def.Actions[d.Category]
	if !ok {
		d.Outcome = OutcomeRecorded
		return
	}
	d.Action = action.Tool
	args, err := substitute(action.Args, item.value, d.data.fetched, d.Category)
	if err != nil {
		*d = d.failed(fmt.Errorf("actions.%s.args: %w", d.Category, err))
		return
	}
	if _, err := s.host.Tools.Call(ctx, scope.AgentID, action.Tool, nonNilArgs(args)); err != nil {
		if text, encErr := encodeArgs(args); encErr == nil {
			d.ActionArgs = text
		}
		*d = d.failed(&toolError{role: roleAction, category: d.Category, tool: action.Tool, err: err})
		d.Error, d.cause = s.host.redact(d.Error), s.redactCause(d.cause)
		return
	}
	d.Outcome = OutcomeActed
}

// encodeArgs is an action's arguments after substitution as compact JSON,
// cut to ActivityValueMax characters.
func encodeArgs(args map[string]any) (string, error) {
	var buf strings.Builder
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(nonNilArgs(args)); err != nil {
		return "", err
	}
	return cutRunes(strings.TrimSuffix(buf.String(), "\n"), ActivityValueMax), nil
}

// previewAction records, without running it, the action the decision's
// category would run: its tool and its arguments after substitution
// (encodeArgs). A substitution error fails the item as it would fail a
// live one.
func previewAction(def *Definition, item listedItem, d *decision) {
	action, ok := def.Actions[d.Category]
	if !ok {
		return
	}
	d.Action = action.Tool
	args, err := substitute(action.Args, item.value, d.data.fetched, d.Category)
	if err != nil {
		*d = d.failed(fmt.Errorf("actions.%s.args: %w", d.Category, err))
		return
	}
	text, err := encodeArgs(args)
	if err != nil {
		*d = d.failed(fmt.Errorf("actions.%s.args: %w", d.Category, err))
		return
	}
	d.ActionArgs = text
}

func nonNilArgs(args map[string]any) map[string]any {
	if args == nil {
		return map[string]any{}
	}
	return args
}

// handleItem decides one item and, by mode, acts and stores the outcome.
// The activity entry and the audit row are written for every mode but test.
//
// State is written with a context that is not cancelled with ctx, so an
// action that ran is recorded even when the run is stopped meanwhile; a
// classification that cannot be recorded fails the item before its action
// runs, so a later run never repeats an action it did not know had run.
func (s *Service) handleItem(ctx context.Context, scope Scope, def *Definition, in instructions, st *stateDB, item listedItem, reuse *processedRow, mode itemMode) decision {
	ctx = withClassifier(ctx, NormalizeID(def.ID))
	dry := mode == modeDry || (mode == modeRerun && def.DryRun)
	d := s.decide(ctx, scope, def, in, item, reuse)
	d.Error, d.cause = s.host.redact(d.Error), s.redactCause(d.cause)
	if d.Outcome == OutcomeFailed && ctx.Err() != nil && mode != modeTest {
		d.interrupted = true // the service stopped: the next run takes the item again
		return d
	}
	now := s.host.clock().Now().UTC()
	d.At, d.DryRun, d.Rerun = now, dry, mode == modeRerun
	cls := NormalizeID(def.ID)
	wctx := context.WithoutCancel(ctx)

	storeErr := func(err error) {
		if err != nil {
			s.host.Logger.Errorf("classifier=%s agent=%s item=%s: store: %v", def.ID, scope.AgentID, item.id, err)
		}
	}
	switch {
	case mode == modeTest:
		if d.Category != "" {
			d.Outcome = OutcomeRecorded
			previewAction(def, item, &d)
		}
		return d
	case dry:
		if d.Outcome == OutcomeFailed {
			break // not recorded: the next dry run tries it again
		}
		category := d.Category
		if d.Outcome == OutcomeUnclassified {
			category = Unclassified
		} else {
			d.Outcome = OutcomeRecorded
			if previewAction(def, item, &d); d.Outcome == OutcomeFailed {
				break // the live action would fail; not recorded, so the next dry run tries it again
			}
		}
		storeErr(st.setDry(wctx, cls, item.id, category, d.Reason, d.Model, now))
	case d.Outcome == OutcomeFailed:
		storeErr(st.setFailed(wctx, cls, item.id, d.Error, now))
	case d.Outcome == OutcomeUnclassified:
		storeErr(st.setUnclassified(wctx, cls, item.id, d.Model, now))
	default:
		if reuse == nil || reuse.category != d.Category || mode == modeRerun {
			if err := st.setClassified(wctx, cls, item.id, d.Category, d.Reason, d.Model, now); err != nil {
				storeErr(err)
				d.Outcome, d.Error = OutcomeFailed, "the classification could not be recorded; the action was not run"
				break
			}
		}
		s.act(ctx, scope, def, item, &d)
		if d.Outcome == OutcomeFailed && ctx.Err() != nil {
			// The service stopped during the action: the item stays
			// classified with its action pending, so the next run runs it.
			d.interrupted = true
			return d
		}
		if d.Outcome == OutcomeFailed {
			storeErr(st.setFailed(wctx, cls, item.id, d.Error, s.host.clock().Now().UTC()))
		} else {
			storeErr(st.setActed(wctx, cls, item.id, s.host.clock().Now().UTC()))
		}
	}

	fields := make(map[string]string, len(d.data.fields))
	for _, f := range d.data.fields {
		fields[f.Name] = f.Value
	}
	storeErr(st.addActivity(wctx, cls, ActivityEntry{Decision: d.Decision, Fields: fields}))
	s.host.audit(AuditEvent{
		Kind: AuditClassification, Agent: scope.AgentID, Classifier: def.ID, ItemID: item.id,
		Category: d.Category, Action: d.Action, Outcome: d.Outcome, Clone: scope.Clone,
		Details: map[string]any{"reason": d.Reason, "model": d.Model, "error": d.Error, "dry_run": d.DryRun, "rerun": d.Rerun},
	})
	if d.Outcome == OutcomeFailed {
		s.host.Logger.Warnf("classifier=%s agent=%s item=%s failed: %s", def.ID, scope.AgentID, item.id, d.Error)
	}
	return d
}

// runClassifier runs one classifier once (design 4.1, 6, 7): it prunes the
// state, checks the definition against the current maximums and the agent's
// grants, lists the items, skips those already handled, and handles up to
// items_per_run of the rest, parallel at a time, in source order. It stops
// starting items after stop_after_failures consecutive failures (a failed
// run, quoting the last item's error), when stop is closed (the classifier
// was disabled or deleted, or the agent's switch turned off: the items in
// flight finish), or when the model cannot be used. A shutdown (ctx
// cancelled) stops it without a failure: what it interrupted is neither
// recorded nor reported. The run is recorded and, as report.summary says,
// summarised to the agent; a failed run is also logged at warn, written to
// the activity log and, as trackFailure decides, reported to the agent
// itself (reportFailure).
func (s *Service) runClassifier(ctx context.Context, scope Scope, def *Definition, stop <-chan struct{}) RunRecord {
	rec := RunRecord{StartedAt: s.host.clock().Now().UTC(), DryRun: def.DryRun, Status: RunOK}
	cls := NormalizeID(def.ID)
	s.host.Logger.Infof("classifier=%s agent=%s: run started", def.ID, scope.AgentID)

	st, err := s.state(ctx, scope.BaseDirectory)
	switch {
	case err != nil && (ctx.Err() != nil || errors.Is(err, ErrClosed)):
		rec.Status, rec.Error, rec.EndedAt = RunStopped, stoppedByService, s.host.clock().Now().UTC()
		s.host.Logger.Infof("classifier=%s agent=%s: run stopped (%s)", def.ID, scope.AgentID, rec.Error)
		return rec
	case err != nil:
		return s.stateFailed(ctx, scope, def, rec, err)
	}

	// cooling: the run ended because the model is in cooldown, which ends
	// by itself; it is not a failure to report to the agent.
	cooling := false
	finish := func() RunRecord {
		rec.EndedAt = s.host.clock().Now().UTC()
		if runErr := st.setRun(context.WithoutCancel(ctx), cls, rec); runErr != nil {
			s.host.Logger.Errorf("classifier=%s agent=%s: store run: %v", def.ID, scope.AgentID, runErr)
		}
		logf := s.host.Logger.Infof
		if rec.Status == RunError {
			logf = s.host.Logger.Warnf
		}
		logf("classifier=%s agent=%s: run %s: %d listed, %d new, %d classified, %d acted, %d unclassified, %d failed%s",
			def.ID, scope.AgentID, rec.Status, rec.Listed, rec.NewItems, rec.Classified, rec.Acted, rec.Unclassified, rec.Failed,
			errSuffix(rec.Error))
		s.afterRun(ctx, scope, def, st, rec, cooling)
		return rec
	}
	failWith := func(text string, c cause) RunRecord {
		if ctx.Err() != nil {
			// The service stopped (a call was cancelled): not a failure.
			rec.Status, rec.Error = RunStopped, stoppedByService
			return finish()
		}
		rec.Status, rec.Error, rec.cause = RunError, s.host.redact(text), s.redactCause(c)
		return finish()
	}
	fail := func(err error) RunRecord { return failWith(err.Error(), causeOf(err)) }

	if !sameAgent(def.Owner, scope.AgentID) {
		return fail(fmt.Errorf("the classifier is owned by %s", def.Owner))
	}
	if err = ValidateStatic(def, s.host.ceilings()); err != nil {
		return fail(err)
	}
	if issues := missingGrants(def, scope.AgentID, s.host.Tools); len(issues) > 0 {
		return failWith(grantsError(issues).Error(), cause{kind: causeToolMissing, subject: issues[0].tool})
	}
	// Read on every run, before the source is called: a missing or
	// unreadable Markdown file, or a prompt over max_prompt_chars, classifies
	// nothing.
	in, err := s.loadInstructions(scope, def)
	if err != nil {
		return fail(err)
	}
	items, err := listItems(ctx, def, scope.AgentID, s.host.Tools)
	if err != nil {
		return fail(err)
	}
	rec.Listed = len(items)

	todo, reuse, err := s.pending(ctx, st, def, items)
	if err != nil {
		if ctx.Err() == nil {
			s.host.Logger.Errorf("classifier=%s agent=%s: read state: %v", def.ID, scope.AgentID, err)
		}
		return failWith("the classifier's state could not be read", cause{kind: causeOperator})
	}
	// After pending marked every listed item seen: an item still listed is
	// never pruned.
	s.prune(ctx, st, def, true)
	if limit := def.itemsPerRun(s.host.ceilings()); limit > 0 && len(todo) > limit {
		todo = todo[:limit]
	}
	rec.NewItems = len(todo)

	mode := modeLive
	if def.DryRun {
		mode = modeDry
	}
	var (
		mu          sync.Mutex
		wg          sync.WaitGroup
		consecutive int
		fatal       error
		stoppedBy   string
		failedOut   bool    // stopped by stop_after_failures: a failed run
		lastCause   cause   // the run's cause when failedOut
		streak      []cause // the causes of the failures in a row
	)
	slots := make(chan struct{}, def.parallel())
	stopping := func() bool {
		mu.Lock()
		defer mu.Unlock()
		if fatal != nil || stoppedBy != "" {
			return true
		}
		// The service context first: a shutdown also closes every run's stop
		// channel, and a select over both would pick either reason at random.
		if ctx.Err() != nil {
			stoppedBy = stoppedByService
			return true
		}
		select {
		case <-stop:
			stoppedBy = "the classifier was stopped"
		default:
		}
		return stoppedBy != ""
	}
	limit := def.stopAfterFailures()
	for _, item := range todo {
		if stopping() {
			break
		}
		select {
		case slots <- struct{}{}:
		case <-ctx.Done():
		}
		// stopping first: it records why the run stopped (a shutdown while
		// waiting for a slot included), so the run is not reported ok.
		if stopping() {
			break
		}
		wg.Add(1)
		go func(item listedItem) {
			defer wg.Done()
			defer func() { <-slots }()
			d := s.handleItem(ctx, scope, def, in, st, item, reuse[item.id], mode)
			mu.Lock()
			defer mu.Unlock()
			if d.interrupted {
				return // not handled: the service stopped
			}
			switch d.Outcome {
			case OutcomeFailed:
				rec.Failed++
				consecutive++
				streak = append(streak, d.cause)
			case OutcomeUnclassified:
				rec.Unclassified++
				consecutive, streak = 0, nil
			default:
				rec.Classified++
				if d.Outcome == OutcomeActed {
					rec.Acted++
				}
				consecutive, streak = 0, nil
			}
			if d.fatal != nil && fatal == nil {
				fatal = d.fatal
			}
			if consecutive >= limit && stoppedBy == "" {
				// The run's error names the cause the streak's failures
				// share most (not any item's text), so the same failure
				// reads the same every run and folds; the last item's full
				// error is the detail.
				lastCause = commonCause(streak)
				stoppedBy = "stopped after " + failurePlural(consecutive) + ": " + lastCause.summary
				lastCause.summary, lastCause.detail = stoppedBy, d.Error
				failedOut = true
			}
		}(item)
	}
	wg.Wait()
	if fatal == nil && (stoppedBy == "" || failedOut) && ctx.Err() != nil {
		// An item in flight was interrupted: the service stopped, which is
		// not a failure even when items had failed in a row.
		stoppedBy, failedOut = stoppedByService, false
	}
	switch {
	case fatal != nil:
		rec.Status, rec.Error, rec.cause = RunError, s.host.redact(fatal.Error()), s.redactCause(causeOf(fatal))
		_, cooling = errors.AsType[*CooldownError](fatal)
	case failedOut:
		rec.Status, rec.Error, rec.cause, rec.lastItemError = RunError, stoppedBy, lastCause, lastCause.detail
	case stoppedBy != "":
		rec.Status, rec.Error = RunStopped, stoppedBy
	}
	return finish()
}

// stoppedByService is the error of a run the service's shutdown stopped.
const stoppedByService = "the service stopped"

// afterRun is what a run's end means for error notices: trackFailure
// counts it, the summary goes to the chat, and the notice to the agent.
func (s *Service) afterRun(ctx context.Context, scope Scope, def *Definition, st *stateDB, rec RunRecord, cooling bool) {
	failure := s.trackFailure(ctx, scope, def, st, rec, cooling)
	s.summarise(ctx, scope, def, rec, failure)
	if failure == failureFirst || failure == failureSecond {
		s.reportFailure(ctx, scope, def, rec, failure)
	}
}

// The agent's notice and the chat line of a state.db that cannot be
// opened: one per agent (its classifiers folder), naming no classifier.
const (
	stateNoticeText  = "Your classifiers can't run: their state could not be opened. Only the operator can fix this; tell the user."
	stateSummaryText = "Classifiers can't run: their state could not be opened."
)

// stateFailed ends a run whose folder's state.db cannot be opened. Per
// folder (so per agent), the first failure since it last opened and again
// once ErrorMute has passed is logged at error, raises the operator alert
// and is reported once to the agent and its chat (report.errors of the
// classifier that met it decides); the failures between are logged at
// debug. The mute is kept in memory, since the count of error notices
// lives in that state.db: a restart can report it once more.
func (s *Service) stateFailed(ctx context.Context, scope Scope, def *Definition, rec RunRecord, err error) RunRecord {
	now := s.host.clock().Now().UTC()
	rec.Status, rec.Error, rec.EndedAt = RunError, ErrStateUnavailable.Error(), now
	rec.cause = cause{kind: causeOperator, summary: rec.Error}
	if !s.stateNotice(scope.BaseDirectory, now) {
		s.host.Logger.Debugf("classifier=%s agent=%s: open state: %v", def.ID, scope.AgentID, err)
		return rec
	}
	s.host.Logger.Errorf("classifier=%s agent=%s: open state: %v", def.ID, scope.AgentID, err)
	if s.host.Notifier == nil {
		return rec
	}
	nctx := withClassifier(ctx, NormalizeID(def.ID))
	s.host.Notifier.StateFailed(nctx, scope.AgentID, filepath.Join(scope.BaseDirectory, classifierfs.StateFile), err)
	if !def.reportErrors() {
		return rec
	}
	if def.summaryMode() != SummaryNever {
		if sErr := s.host.Notifier.Summary(nctx, scope.AgentID, stateSummaryText); sErr != nil {
			s.host.Logger.Warnf("classifier=%s agent=%s: summary not delivered: %v", def.ID, scope.AgentID, sErr)
		}
	}
	if nErr := s.host.Notifier.RunFailed(nctx, scope.AgentID, stateNoticeText); nErr != nil {
		s.host.Logger.Warnf("classifier=%s agent=%s: failure notice not delivered: %v", def.ID, scope.AgentID, nErr)
	}
	return rec
}

// stateNotice reports whether a folder whose state cannot be opened is
// reported now: the first time since it last opened, then once per
// ErrorMute.
func (s *Service) stateNotice(base string, now time.Time) bool {
	base = filepath.Clean(base)
	s.mu.Lock()
	defer s.mu.Unlock()
	if until, ok := s.stateMuted[base]; ok && now.Before(until) {
		return false
	}
	s.stateMuted[base] = now.Add(ErrorMute)
	return true
}

func errSuffix(e string) string {
	if e == "" {
		return ""
	}
	return " (" + e + ")"
}

// pending returns the items a run takes, in source order (design 4.1d, 7.1),
// each id once: a live run skips items acted or unclassified and takes again
// those classified with the action pending (reusing their category) and
// failed; a dry run skips items with a dry-run decision. Every listed item's
// row is marked seen now, so retention runs from when it leaves the list.
func (s *Service) pending(ctx context.Context, st *stateDB, def *Definition, items []listedItem) ([]listedItem, map[string]*processedRow, error) {
	cls := NormalizeID(def.ID)
	reuse := map[string]*processedRow{}
	var todo []listedItem
	var unique []listedItem
	seen := map[string]bool{}
	ids := make([]string, 0, len(items))
	for _, it := range items {
		if seen[it.id] {
			continue // a list naming an item twice handles it once
		}
		seen[it.id] = true
		unique = append(unique, it)
		ids = append(ids, it.id)
	}
	table := "processed"
	if def.DryRun {
		table = "dry_run"
	}
	if err := st.markSeen(ctx, table, cls, ids, s.host.clock().Now()); err != nil {
		return nil, nil, err
	}
	if def.DryRun {
		decided, err := st.dryDecided(ctx, cls)
		if err != nil {
			return nil, nil, err
		}
		for _, it := range unique {
			if !decided[it.id] {
				todo = append(todo, it)
			}
		}
		return todo, reuse, nil
	}
	rows, err := st.processed(ctx, cls)
	if err != nil {
		return nil, nil, err
	}
	for _, it := range unique {
		row, ok := rows[it.id]
		if !ok {
			todo = append(todo, it)
			continue
		}
		switch row.status {
		case StatusActed, StatusUnclassified:
			continue
		default:
			r := row
			reuse[it.id] = &r
			todo = append(todo, it)
		}
	}
	return todo, reuse, nil
}

// prune deletes activity older than the definition's activity_hours and,
// with processed set (a run, once the listed items are marked seen),
// processed and dry-run rows of items not listed within the processed-id
// retention.
func (s *Service) prune(ctx context.Context, st *stateDB, def *Definition, processed bool) {
	now := s.host.clock().Now()
	var processedBefore time.Time
	if processed {
		processedBefore = now.Add(-time.Duration(s.host.ceilings().processedDays()) * 24 * time.Hour)
	}
	activityBefore := now.Add(-time.Duration(def.activityHours()) * time.Hour)
	if err := st.prune(ctx, NormalizeID(def.ID), processedBefore, activityBefore); err != nil {
		s.host.Logger.Errorf("classifier=%s: prune state: %v", def.ID, err)
	}
}

// ErrorMute is how long a classifier's error notices stay muted after its
// second notice in a row. It is fixed: the agent lifts the mute by changing
// the definition (classifier_update), and a failure after it has expired
// is reported as a first notice again.
const ErrorMute = 24 * time.Hour

// failureReport is what a run's end means for error notices.
type failureReport int

const (
	failureNone   failureReport = iota // the run did not fail (or only its model is in cooldown)
	failureSilent                      // failed; only logged (muted, or report.errors is false)
	failureFirst                       // failed; the first notice
	failureSecond                      // failed; the second notice, which mutes the next ones for ErrorMute
)

// trackFailure logs a failed run in the activity log and decides whether it
// is reported. Per classifier, the first failed run sends a notice, the
// second sends one saying that further errors are muted for ErrorMute, and
// later ones are only logged until the mute has expired when one happens
// (no timer: muted_until is compared with the failure's time). A run that
// succeeds resets the count but not the mute; a change of the definition by
// the agent resets both (Service.Update, Create). A stopped run (the
// classifier or the service stopped it) is neither: it changes nothing, so
// the same failure after it folds into the same row and counts on.
// report.errors false reports none. The count and the mute are kept in
// state.db, so a restart keeps them. A run that ended on a model in
// cooldown changes nothing: the cooldown ends by itself, and the item it
// failed is in the log already.
func (s *Service) trackFailure(ctx context.Context, scope Scope, def *Definition, st *stateDB, rec RunRecord, cooling bool) failureReport {
	if cooling {
		return failureNone
	}
	wctx := context.WithoutCancel(ctx)
	cls := NormalizeID(def.ID)
	storeErr := func(what string, err error) {
		if err != nil {
			s.host.Logger.Errorf("classifier=%s agent=%s: %s: %v", def.ID, scope.AgentID, what, err)
		}
	}
	switch rec.Status {
	case RunOK:
		storeErr("store run failures", st.runSucceeded(wctx, cls))
		return failureNone
	case RunError:
	default:
		return failureNone
	}
	now := s.host.clock().Now().UTC()
	storeErr("store the failed run", st.addRunFailure(wctx, cls, rec.Error, rec.lastItemError, rec.DryRun, now))
	f, err := runFailuresFrom(wctx, st.db, cls)
	storeErr("read run failures", err) // read as none: reported rather than never
	if !f.mutedUntil.IsZero() && !now.Before(f.mutedUntil) {
		f = failureState{} // the mute has expired: a new cycle
	}
	f.failures++
	report := failureSilent
	if f.mutedUntil.IsZero() && def.reportErrors() {
		report = failureFirst
		if f.failures > 1 {
			report, f.mutedUntil = failureSecond, now.Add(ErrorMute)
		}
	}
	storeErr("store run failures", st.setRunFailures(wctx, cls, f))
	return report
}

// reportFailure delivers the notice of a failed run to the owning agent,
// whatever report.summary says, so it can fix the classifier; the second
// one in a row also says that further errors are muted and how to report
// them again. The notice's sentences are ClawEh's own (failureText, the
// advice, the mute); what a tool, a provider or an item said (the cause's
// detail) follows only inside Host.WrapUntrusted, as data.
func (s *Service) reportFailure(ctx context.Context, scope Scope, def *Definition, rec RunRecord, failure failureReport) {
	if s.host.Notifier == nil {
		return
	}
	text := failureText(def, rec) + " " + advice(rec.cause)
	if failure == failureSecond {
		text += " " + muteNotice
	}
	if rec.cause.detail != "" {
		text += "\n" + s.host.wrapUntrusted(rec.cause.detail)
	}
	if err := s.host.Notifier.RunFailed(withClassifier(ctx, NormalizeID(def.ID)), scope.AgentID, text); err != nil {
		s.host.Logger.Warnf("classifier=%s agent=%s: failure notice not delivered: %v", def.ID, scope.AgentID, err)
		return
	}
	s.host.Logger.Infof("classifier=%s agent=%s: run failure reported to the agent", def.ID, scope.AgentID)
}

// muteHours is ErrorMute in whole hours, for the notices.
var muteHours = int(ErrorMute / time.Hour)

// muteNotice ends the agent's second notice in a row; muteSummary ends the
// chat summary's.
var (
	muteNotice = fmt.Sprintf("Further errors from this classifier won't be reported for %d hours. "+
		`To report them again now, call classifier_update with {"report": {"errors": true}}.`, muteHours)
	muteSummary = fmt.Sprintf("Further errors won't be reported for %d hours.", muteHours)
)

// summarise posts the run's summary to the owning agent's chat when
// report.summary asks for it: always (a run that handled items or failed),
// on_problems (default: a run with unclassified or failed items, or that
// failed), never. A failed run is posted only when trackFailure reports it
// (failure), with the same count and mute as the agent's notice.
func (s *Service) summarise(ctx context.Context, scope Scope, def *Definition, rec RunRecord, failure failureReport) {
	if s.host.Notifier == nil {
		return
	}
	text := summaryText(def, rec)
	switch mode := def.summaryMode(); {
	case mode == SummaryNever, failure == failureSilent:
		return
	case failure == failureFirst:
		text = failureText(def, rec)
	case failure == failureSecond:
		text = failureText(def, rec) + " " + muteSummary
	case mode == SummaryAlways:
		if rec.NewItems == 0 && rec.Status != RunError {
			return
		}
	default:
		if rec.Unclassified+rec.Failed == 0 {
			return
		}
	}
	if err := s.host.Notifier.Summary(withClassifier(ctx, NormalizeID(def.ID)), scope.AgentID, text); err != nil {
		s.host.Logger.Warnf("classifier=%s agent=%s: summary not delivered: %v", def.ID, scope.AgentID, err)
	}
}

// summaryText is the one-paragraph summary of a run.
func summaryText(def *Definition, rec RunRecord) string {
	var b strings.Builder
	b.WriteString("Classifier " + def.ID)
	if rec.DryRun {
		b.WriteString(" (dry run)")
	}
	fmt.Fprintf(&b, ": %d new items, %d acted, %d unclassified, %d failed", rec.NewItems, rec.Acted, rec.Unclassified, rec.Failed)
	b.WriteString(errSuffix(rec.Error))
	b.WriteString(". Use classifier_activity for details.")
	return b.String()
}

// failureText is the one-sentence report of a failed run in ClawEh's own
// words, naming the classifier and the cause's summary: "Classifier inbox:
// the run failed (source mail_list: the tool failed).", or for a definition
// file that is not valid "Classifier inbox is not run (<why>)." It never
// holds what a tool, a provider or an item said: it is the person's chat
// line and the start of the agent's notice.
func failureText(def *Definition, rec RunRecord) string {
	var b strings.Builder
	b.WriteString("Classifier " + def.ID)
	if rec.DryRun {
		b.WriteString(" (dry run)")
	}
	if rec.cause.kind == causeInvalidFile {
		b.WriteString(" is not run (")
	} else {
		b.WriteString(": the run failed (")
	}
	msg := rec.cause.summary
	if msg == "" {
		msg = rec.Error
	}
	msg = strings.TrimSuffix(strings.Join(strings.Fields(msg), " "), ".") // a validation error has one issue per line
	b.WriteString(msg)
	b.WriteString(").")
	return b.String()
}
