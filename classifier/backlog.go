// ClawEh
// License: MIT
//
// Copyright (c) 2026 Tenebris Technologies Inc.

package classifier

import "context"

// What a run takes of the items its source lists.
//
// An item is handled once: one that is done (acted, or left unclassified) is
// skipped for good, by its id. A run takes, in this order, the items whose
// action is pending (classified already: no model call), the new ones in
// source order, then the failed ones. Failed items come last so that items
// that keep failing never stand between a run and the new ones, whatever
// order the source lists in.
//
// A failed item is tried again until limits.max_attempts attempts were
// counted for it; after that it is left alone ("given up on") until the
// agent changes the definition (Service.Create, Update) or reruns it. An
// attempt is counted only in a run that handled at least one other item
// without failing (countAttempts): a run in which nothing worked says the
// tools or the model are down, not that its items are at fault, so an outage
// never uses up an item's attempts.

// knownItems is what the state holds about a classifier's items when a run
// starts: live, every processed row; dry run, the ids decided and the
// attempts of those that failed.
type knownItems struct {
	dry         bool
	maxAttempts int
	rows        map[string]processedRow // live
	decided     map[string]bool         // dry run
	dryFailed   map[string]int          // dry run: counted attempts by id
}

// known reads what the state holds about def's items.
func (s *Service) known(ctx context.Context, st *stateDB, def *Definition) (knownItems, error) {
	cls := NormalizeID(def.ID)
	k := knownItems{dry: def.DryRun, maxAttempts: def.maxAttempts()}
	var err error
	if k.dry {
		if k.decided, err = st.dryDecided(ctx, cls); err != nil {
			return knownItems{}, err
		}
		k.dryFailed, err = st.dryFailed(ctx, cls)
		return k, err
	}
	k.rows, err = st.processed(ctx, cls)
	return k, err
}

// What a run does with a listed item.
const (
	takeSkip    = iota // done, or given up on
	takePending        // classified, its action pending
	takeNew            // never handled
	takeRetry          // failed, attempts left
)

// take says what a run does with the item id.
func (k knownItems) take(id string) int {
	if k.dry {
		attempts, failed := k.dryFailed[id]
		switch {
		case k.decided[id]:
			return takeSkip
		case !failed:
			return takeNew
		case attempts >= k.maxAttempts:
			return takeSkip
		}
		return takeRetry
	}
	row, ok := k.rows[id]
	switch {
	case !ok:
		return takeNew
	case row.status == StatusActed, row.status == StatusUnclassified:
		return takeSkip
	case row.status == StatusClassified:
		return takePending
	case row.attempts >= k.maxAttempts:
		return takeSkip
	}
	return takeRetry
}

// first reports whether a run takes the item id ahead of its retries: it is
// new, or its action is pending. A listing has enough once it found
// items_per_run of these.
func (k knownItems) first(id string) bool {
	t := k.take(id)
	return t == takePending || t == takeNew
}

// pending returns the items a run takes of those listed (each id once), in
// the order it takes them, and for a live run the stored row of each item
// handled before, whose answer is reused.
func (k knownItems) pending(items []listedItem) ([]listedItem, map[string]*processedRow) {
	reuse := map[string]*processedRow{}
	var pending, fresh, retry []listedItem
	for _, it := range items {
		t := k.take(it.id)
		if t == takeSkip {
			continue
		}
		if row, ok := k.rows[it.id]; ok {
			reuse[it.id] = &row
		}
		switch t {
		case takePending:
			pending = append(pending, it)
		case takeNew:
			fresh = append(fresh, it)
		default:
			retry = append(retry, it)
		}
	}
	return append(append(pending, fresh...), retry...), reuse
}

// markSeen marks every listed item's rows seen now, so retention runs from
// when an item leaves the list.
func (s *Service) markSeen(ctx context.Context, st *stateDB, def *Definition, items []listedItem) error {
	tables := []string{tableProcessed}
	if def.DryRun {
		tables = []string{tableDryRun, tableDryFailed}
	}
	ids, now := itemIDs(items), s.host.clock().Now()
	for _, table := range tables {
		if err := st.markSeen(ctx, table, NormalizeID(def.ID), ids, now); err != nil {
			return err
		}
	}
	return nil
}

// countAttempts counts an attempt for each item of failed (the items that
// failed in the run rec, not those a model that cannot be used failed) when
// the run handled at least one item without failing, and logs how many items
// it gave up on.
func (s *Service) countAttempts(ctx context.Context, scope Scope, def *Definition, st *stateDB, rec RunRecord, failed []string) {
	if len(failed) == 0 || rec.Classified+rec.Unclassified == 0 {
		return
	}
	wctx := context.WithoutCancel(ctx)
	cls := NormalizeID(def.ID)
	table := tableProcessed
	if def.DryRun {
		table = tableDryFailed
	}
	given := func() int {
		live, dry, err := st.gaveUp(wctx, cls, def.maxAttempts())
		if err != nil {
			return -1
		}
		if def.DryRun {
			return dry
		}
		return live
	}
	before := given()
	if err := st.addAttempts(wctx, table, cls, failed); err != nil {
		s.host.Logger.Errorf("classifier=%s agent=%s: count attempts: %v", def.ID, scope.AgentID, err)
		return
	}
	if after := given(); before >= 0 && after > before {
		s.host.Logger.Warnf("classifier=%s agent=%s: %d item(s) failed %d times and are not tried again",
			def.ID, scope.AgentID, after-before, def.maxAttempts())
	}
}
