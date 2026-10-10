// ClawEh
// License: MIT
//
// Copyright (c) 2026 Tenebris Technologies Inc.

package forum

import (
	"context"
	"errors"
	"slices"
	"sort"
	"time"
)

// Reading forums (forum_status, forum_results): summaries and results,
// from the live controller when the run is active here, else from disk.

// Status returns the summary of one run of a forum (its latest when run
// is 0), with the forum's run count and whether its configuration changed
// since the latest run; a forum with no run is StatusNew. It reads the
// records without verifying their digests (loadView); verification is
// openForum's job.
func (s *Service) Status(_ context.Context, scope Scope, id string, run int) (*Summary, error) {
	store, err := s.open(scope, id)
	if err != nil {
		return nil, err
	}
	raw, mod, err := store.ReadForumConfig()
	if err != nil {
		return nil, err
	}
	runs, err := startedRuns(store)
	if err != nil {
		return nil, err
	}
	latest := 0
	if len(runs) > 0 {
		latest = runs[len(runs)-1]
	}
	if run == 0 {
		run = latest
	}
	if run == 0 {
		return &Summary{ForumID: id, Name: configName(raw), Status: StatusNew, UpdatedAt: mod, Layers: []LayerProgress{}}, nil
	}
	if !slices.Contains(runs, run) {
		return nil, errNoRun(Ref(configName(raw), id), run)
	}
	var sum *Summary
	if r, ok := s.running(scope, id); ok && r.store.RunNumber() == run {
		st := r.ctrl.State()
		sum = summaryOf(r.ctrl.Config(), r.ctrl.Snapshot(), &st)
	} else {
		cfg, snap, st, viewErr := loadView(store.Run(run))
		if viewErr != nil {
			return nil, viewErr
		}
		sum = summaryOf(cfg, snap, st)
	}
	sum.Runs = len(runs)
	if sum.ConfigChanged, err = configChanged(store.Run(latest), raw); err != nil {
		return nil, err
	}
	return sum, nil
}

// List returns the summary of every forum in the scope (its latest run),
// newest first (a new forum by when its configuration last changed). A
// forum that cannot be read is logged and left out.
func (s *Service) List(ctx context.Context, scope Scope) ([]Summary, error) {
	ids, err := listForums(scope.BaseDirectory)
	if err != nil {
		return nil, err
	}
	out := make([]Summary, 0, len(ids))
	for _, id := range ids {
		sum, err := s.Status(ctx, scope, id, 0)
		if err != nil {
			s.host.Logger.Warnf("forum %s: status: %v", s.ref(scope, id), err)
			continue
		}
		out = append(out, *sum)
	}
	sort.SliceStable(out, func(i, j int) bool { return sortTime(&out[i]).After(sortTime(&out[j])) })
	return out, nil
}

// sortTime is when a forum's latest run was launched, or a new forum's
// configuration last changed.
func sortTime(sum *Summary) time.Time {
	if sum.Status == StatusNew {
		return sum.UpdatedAt
	}
	return sum.LaunchedAt
}

// Results returns the result of one run of a forum (its latest when run is
// 0): result.json for a terminal run, or a partial manifest for any other.
// Both come from buildResult, which shows a result layer's published
// outputs only and the other layers' outputs only when the result layers
// have none. A forum with no run is refused.
func (s *Service) Results(_ context.Context, scope Scope, id string, run int) (*Result, error) {
	if r, ok := s.running(scope, id); ok && (run == 0 || run == r.store.RunNumber()) {
		if st := r.ctrl.State(); !st.Status.Terminal() {
			return buildResult(r.ctrl.Config(), r.ctrl.Snapshot(), &st), nil
		}
	}
	store, err := s.open(scope, id)
	if err != nil {
		return nil, err
	}
	runs, err := startedRuns(store)
	if err != nil {
		return nil, err
	}
	switch {
	case len(runs) == 0:
		return nil, errNotLaunched(storeRef(store))
	case run == 0:
		run = runs[len(runs)-1]
	case !slices.Contains(runs, run):
		return nil, errNoRun(storeRef(store), run)
	}
	rs := store.Run(run)
	// The run's own records are checked like the latest run's (open):
	// its snapshot must name this run and the scope's agent.
	snap, err := rs.ReadSnapshot()
	if err != nil {
		return nil, corrupt("run %d %s: %v", run, fileSnapshot, err)
	}
	if err = checkOwner(rs, snap); err != nil {
		return nil, err
	}
	if snap.ForumID != id || snap.Run != run {
		return nil, corrupt("run %d %s names forum %s run %d", run, fileSnapshot, snap.ForumID, snap.Run)
	}
	res, err := rs.ReadResult()
	if err == nil {
		if res.ForumID != id || res.Run != run {
			return nil, corrupt("run %d %s names forum %s run %d", run, fileResult, res.ForumID, res.Run)
		}
		return res, nil
	}
	if !errors.Is(err, ErrNotFound) {
		return nil, err
	}
	cfg, snap, st, err := loadView(rs)
	if err != nil {
		return nil, err
	}
	return buildResult(cfg, snap, st), nil
}

// loadView reads what a read-only caller (status, list, results) needs:
// forum.json, snapshot.json and the current State (loadState), without
// verify's digest checks of every source and output.
func loadView(store *forumStore) (*Config, *Snapshot, *State, error) {
	snap, err := store.ReadSnapshot()
	if err != nil {
		return nil, nil, nil, corrupt("%s: %v", fileSnapshot, err)
	}
	if ownerErr := checkOwner(store, snap); ownerErr != nil {
		return nil, nil, nil, ownerErr
	}
	raw, err := store.ReadConfig()
	if err != nil {
		return nil, nil, nil, corrupt("%s: %v", fileConfig, err)
	}
	cfg, err := decodeConfig(raw)
	if err != nil {
		return nil, nil, nil, corrupt("%s: %v", fileConfig, err)
	}
	st, err := loadState(store, cfg, snap)
	if err != nil {
		return nil, nil, nil, err
	}
	return cfg, snap, st, nil
}

// load reads a forum's configuration, snapshot and current state after
// verifying the directory (verify).
func load(store *forumStore) (*Config, *Snapshot, *State, error) {
	cfg, snap, err := verify(store)
	if err != nil {
		return nil, nil, nil, err
	}
	st, err := loadState(store, cfg, snap)
	if err != nil {
		return nil, nil, nil, err
	}
	return cfg, snap, st, nil
}

// summaryOf builds a Summary from the loaded records: every configured
// layer in configuration order, with progress for the enabled ones.
func summaryOf(cfg *Config, snap *Snapshot, st *State) *Summary {
	sum := &Summary{
		ForumID:    snap.ForumID,
		Name:       snap.Name,
		Run:        snap.Run,
		Status:     st.Status,
		Reason:     st.Reason,
		LaunchedAt: snap.LaunchedAt,
		UpdatedAt:  st.UpdatedAt,
		Deadline:   snap.Deadline,
		Calls:      st.Calls,
		MaxCalls:   snap.Limits.MaxCalls,
		Layers:     make([]LayerProgress, 0, len(cfg.Layers)),
	}
	for _, l := range cfg.Layers {
		p := LayerProgress{LayerID: l.ID, Enabled: slices.Contains(snap.Layers, l.ID), MaxRounds: l.MaxRounds}
		if ls := st.Layers[l.ID]; ls != nil {
			p.Started, p.Ended, p.EndReason = ls.Started, ls.Ended, ls.EndReason
			p.Round, p.Calls, p.Outputs = ls.Round, ls.Calls, len(ls.Outputs)
		}
		sum.Layers = append(sum.Layers, p)
	}
	sum.ResentAfterRestart = resentAfterRestart(st)
	return sum
}

// resentAfterRestart counts the committed outputs of every layer whose turn
// was sent again after a restart (OutputRecord.Resent): the one count
// forum_status and the completion notice give.
func resentAfterRestart(st *State) int {
	n := 0
	for _, ls := range st.Layers {
		if ls == nil {
			continue
		}
		for _, o := range ls.Outputs {
			if o.Resent {
				n++
			}
		}
	}
	return n
}
