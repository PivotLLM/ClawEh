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
	"strings"
	"sync"
	"time"
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
// run-wide error (no usable model, cooldown) that ends the run.
type decision struct {
	Decision
	data  itemData
	fatal error
}

// decide fetches one item and classifies it. reuse, when it holds a
// category the definition still has, is the first attempt's answer (design
// 7.1b): no model call is made.
func (s *Service) decide(ctx context.Context, scope Scope, def *Definition, item listedItem, reuse *processedRow) decision {
	var d decision
	d.ItemID = item.id
	if def.Fetch != nil {
		args, err := substitute(def.Fetch.Args, item.value, nil, "")
		if err != nil {
			return d.failed(fmt.Errorf("fetch.args: %w", err))
		}
		result, err := s.host.Tools.Call(ctx, scope.AgentID, def.Fetch.Tool, nonNilArgs(args))
		if err != nil {
			return d.failed(fmt.Errorf("fetch %s: %w", def.Fetch.Tool, err))
		}
		d.data = fieldsFromFetch(result, def.Fetch)
	} else {
		d.data = fieldsFromElement(item.value)
	}
	d.data.fields = truncate(d.data.fields, def.itemBudget(s.host.ceilings()))

	if reuse != nil && reuse.category != "" {
		if c, ok := def.categoryNamed(reuse.category); ok {
			d.Category, d.Reason, d.Model = c.Name, reuse.reason, reuse.model
			return d
		}
	}

	delim := newDelimiter()
	system, user := buildPrompt(def, d.data, "", delim)
	reply, model, err := s.host.Model.Complete(ctx, scope.AgentID, system, user)
	if err != nil {
		return d.modelFailed(err)
	}
	d.Model = model
	category, reason, ok := parseReply(reply, def)
	if !ok {
		system, user = buildPrompt(def, d.data, reply, newDelimiter())
		reply, model, err = s.host.Model.Complete(ctx, scope.AgentID, system, user)
		if err != nil {
			return d.modelFailed(err)
		}
		d.Model = model
		if category, reason, ok = parseReply(reply, def); !ok {
			d.Outcome = OutcomeUnclassified
			return d
		}
	}
	d.Category, d.Reason = category, reason
	return d
}

func (d decision) failed(err error) decision {
	d.Outcome, d.Error = OutcomeFailed, err.Error()
	return d
}

// modelFailed is a model call that failed: the item fails; no usable model
// and a cooldown also end the run (fatal), so the other items are not each
// sent to fail.
func (d decision) modelFailed(err error) decision {
	if _, cooling := errors.AsType[*CooldownError](err); cooling || errors.Is(err, ErrNoModel) {
		d.fatal = err
	}
	return d.failed(err)
}

// act runs the action for the decision's category (design 6): none is
// recorded only; a substitution or tool error fails the item.
func (s *Service) act(ctx context.Context, scope Scope, def *Definition, item listedItem, d *decision) {
	action, ok := def.Actions[d.Category]
	if !ok {
		d.Outcome = OutcomeRecorded
		return
	}
	d.Action = action.Tool
	args, err := substitute(action.Args, item.value, d.data.fetched, d.Category)
	if err != nil {
		d.Outcome, d.Error = OutcomeFailed, fmt.Sprintf("actions.%s.args: %v", d.Category, err)
		return
	}
	if _, err := s.host.Tools.Call(ctx, scope.AgentID, action.Tool, nonNilArgs(args)); err != nil {
		d.Outcome, d.Error = OutcomeFailed, fmt.Sprintf("action %s: %v", action.Tool, err)
		return
	}
	d.Outcome = OutcomeActed
}

// previewAction records, without running it, the action the decision's
// category would run: its tool and its arguments after substitution, as
// compact JSON cut to ActivityValueMax characters. A substitution error
// fails the item as it would fail a live one.
func previewAction(def *Definition, item listedItem, d *decision) {
	action, ok := def.Actions[d.Category]
	if !ok {
		return
	}
	d.Action = action.Tool
	args, err := substitute(action.Args, item.value, d.data.fetched, d.Category)
	if err != nil {
		d.Outcome, d.Error = OutcomeFailed, fmt.Sprintf("actions.%s.args: %v", d.Category, err)
		return
	}
	var buf strings.Builder
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(nonNilArgs(args)); err != nil {
		d.Outcome, d.Error = OutcomeFailed, fmt.Sprintf("actions.%s.args: %v", d.Category, err)
		return
	}
	d.ActionArgs = cutRunes(strings.TrimSuffix(buf.String(), "\n"), ActivityValueMax)
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
func (s *Service) handleItem(ctx context.Context, scope Scope, def *Definition, st *stateDB, item listedItem, reuse *processedRow, mode itemMode) decision {
	ctx = withClassifier(ctx, NormalizeID(def.ID))
	dry := mode == modeDry || (mode == modeRerun && def.DryRun)
	d := s.decide(ctx, scope, def, item, reuse)
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
// starting items after stop_after_failures consecutive failures, when stop is
// closed (the classifier was disabled or deleted, or the agent's switch
// turned off: the items in flight finish), or when the model cannot be used.
// The run is recorded and, as report.summary says, summarised to the agent.
func (s *Service) runClassifier(ctx context.Context, scope Scope, def *Definition, stop <-chan struct{}) RunRecord {
	rec := RunRecord{StartedAt: s.host.clock().Now().UTC(), DryRun: def.DryRun, Status: RunOK}
	cls := NormalizeID(def.ID)
	s.host.Logger.Infof("classifier=%s agent=%s: run started", def.ID, scope.AgentID)

	st, err := s.state(ctx, scope.BaseDirectory)
	if err != nil {
		s.host.Logger.Errorf("classifier=%s agent=%s: open state: %v", def.ID, scope.AgentID, err)
		rec.Status, rec.Error, rec.EndedAt = RunError, "the classifier's state could not be opened", s.host.clock().Now().UTC()
		return rec
	}

	finish := func() RunRecord {
		rec.EndedAt = s.host.clock().Now().UTC()
		if runErr := st.setRun(context.WithoutCancel(ctx), cls, rec); runErr != nil {
			s.host.Logger.Errorf("classifier=%s agent=%s: store run: %v", def.ID, scope.AgentID, runErr)
		}
		s.host.Logger.Infof("classifier=%s agent=%s: run %s: %d listed, %d new, %d classified, %d acted, %d unclassified, %d failed%s",
			def.ID, scope.AgentID, rec.Status, rec.Listed, rec.NewItems, rec.Classified, rec.Acted, rec.Unclassified, rec.Failed,
			errSuffix(rec.Error))
		s.summarise(ctx, scope, def, rec)
		return rec
	}
	fail := func(cause error) RunRecord {
		rec.Status, rec.Error = RunError, cause.Error()
		return finish()
	}

	if !sameAgent(def.Owner, scope.AgentID) {
		return fail(fmt.Errorf("the classifier is owned by %s", def.Owner))
	}
	if err = ValidateStatic(def, s.host.ceilings()); err != nil {
		return fail(err)
	}
	if err = validateGrants(def, scope.AgentID, s.host.Tools); err != nil {
		return fail(err)
	}
	items, err := listItems(ctx, def, scope.AgentID, s.host.Tools)
	if err != nil {
		return fail(err)
	}
	rec.Listed = len(items)

	todo, reuse, err := s.pending(ctx, st, def, items)
	if err != nil {
		s.host.Logger.Errorf("classifier=%s agent=%s: read state: %v", def.ID, scope.AgentID, err)
		return fail(errors.New("the classifier's state could not be read"))
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
			stoppedBy = "the service stopped"
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
			d := s.handleItem(ctx, scope, def, st, item, reuse[item.id], mode)
			mu.Lock()
			defer mu.Unlock()
			switch d.Outcome {
			case OutcomeFailed:
				rec.Failed++
				consecutive++
			case OutcomeUnclassified:
				rec.Unclassified++
				consecutive = 0
			default:
				rec.Classified++
				if d.Outcome == OutcomeActed {
					rec.Acted++
				}
				consecutive = 0
			}
			if d.fatal != nil && fatal == nil {
				fatal = d.fatal
			}
			if consecutive >= limit && stoppedBy == "" {
				stoppedBy = fmt.Sprintf("stopped after %d consecutive failures", consecutive)
			}
		}(item)
	}
	wg.Wait()
	switch {
	case fatal != nil:
		rec.Status, rec.Error = RunError, fatal.Error()
	case stoppedBy != "":
		rec.Status, rec.Error = RunStopped, stoppedBy
	}
	return finish()
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

// summarise posts the run's summary to the owning agent's chat when
// report.summary asks for it: always (a run that handled items or failed),
// on_problems (default: a run with unclassified or failed items), never.
func (s *Service) summarise(ctx context.Context, scope Scope, def *Definition, rec RunRecord) {
	if s.host.Notifier == nil {
		return
	}
	switch def.summaryMode() {
	case SummaryNever:
		return
	case SummaryAlways:
		if rec.NewItems == 0 && rec.Status != RunError {
			return
		}
	default:
		if rec.Unclassified+rec.Failed == 0 {
			return
		}
	}
	if err := s.host.Notifier.Summary(withClassifier(ctx, NormalizeID(def.ID)), scope.AgentID, summaryText(def, rec)); err != nil {
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
