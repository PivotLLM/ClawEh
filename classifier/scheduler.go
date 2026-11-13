// ClawEh
// License: MIT
//
// Copyright (c) 2026 Tenebris Technologies Inc.

package classifier

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"
	"unicode"
	"unicode/utf8"
)

// The scheduler (design 8): every enabled, valid classifier of an agent
// whose switch is on runs every trigger.every_seconds on its own timer, one
// run at a time; a trigger during a run is coalesced into one more run right
// after it. The table follows the definition files: a new, changed or
// removed file is picked up at the next tick (mtime and size, as the cron
// store does), an invalid file is logged and reported to its agent once
// per change (reportInvalid) and never run or deleted. The scopes are re-read every scopeRefresh and on Reload; an agent
// that left them has its runs stopped (definitions are kept).

const (
	tickInterval = time.Second
	scopeRefresh = 10 * time.Second
)

// entry is one definition file in the scheduler's table.
type entry struct {
	key     string
	scope   Scope
	def     *Definition // nil when invalid or not run by this scope
	invalid string
	modTime time.Time
	size    int64
	nextDue time.Time
	running bool
	pending bool
	stop    chan struct{} // closed to stop the run in progress
}

// stopRun stops the entry's run in progress, if any: the items in flight
// finish and no more start.
func (e *entry) stopRun() {
	if e.stop != nil {
		close(e.stop)
		e.stop = nil
	}
}

// Start starts the scheduler; it runs until ctx ends or Close. Calling it
// again does nothing.
func (s *Service) Start(ctx context.Context) {
	s.mu.Lock()
	if s.started || s.closing {
		s.mu.Unlock()
		return
	}
	s.started = true
	ctx, s.cancel = context.WithCancel(ctx)
	s.reloadRequested = true
	s.wg.Add(1)
	s.mu.Unlock()
	go s.loop(ctx)
}

// Reload re-reads the scopes now (a configuration reload: switches and
// models changed).
func (s *Service) Reload() {
	s.mu.Lock()
	s.reloadRequested = true
	s.mu.Unlock()
	s.wake()
}

// wake makes the scheduler tick now.
func (s *Service) wake() {
	select {
	case s.kick <- struct{}{}:
	default:
	}
}

func (s *Service) loop(ctx context.Context) {
	defer s.wg.Done()
	clock := s.host.clock()
	for {
		s.tick(ctx)
		select {
		case <-ctx.Done():
			s.mu.Lock()
			for _, e := range s.entries {
				e.stopRun()
			}
			s.mu.Unlock()
			return
		case <-clock.After(tickInterval):
		case <-s.kick:
		}
	}
}

// tick refreshes the scopes when due, follows the definition files and
// starts the runs that are due.
func (s *Service) tick(ctx context.Context) {
	now := s.host.clock().Now()
	s.mu.Lock()
	refresh := s.reloadRequested || now.Sub(s.lastScopes) >= scopeRefresh
	s.mu.Unlock()
	if refresh {
		var scopes []Scope
		if s.host.Scopes != nil {
			scopes = s.host.Scopes()
		}
		s.mu.Lock()
		s.scopes, s.lastScopes, s.reloadRequested = scopes, now, false
		s.mu.Unlock()
	}

	s.mu.Lock()
	scopes := s.scopes
	s.mu.Unlock()
	byBase := map[string][]Scope{}
	var bases []string
	for _, sc := range scopes {
		base := filepath.Clean(sc.BaseDirectory)
		if _, ok := byBase[base]; !ok {
			bases = append(bases, base)
		}
		byBase[base] = append(byBase[base], sc)
	}
	seen := map[string]bool{}
	for _, base := range bases {
		s.scanBase(ctx, base, byBase[base], seen)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	for key, e := range s.entries {
		if !seen[key] {
			e.stopRun()
			delete(s.entries, key)
		}
	}
	for _, e := range s.entries {
		if e.def == nil || !e.def.Enabled {
			e.stopRun()
			continue
		}
		if now.Before(e.nextDue) {
			continue
		}
		e.nextDue = now.Add(time.Duration(e.def.Trigger.EverySeconds) * time.Second)
		if e.running {
			e.pending = true
			continue
		}
		s.startLocked(ctx, e)
	}
}

// scanBase follows the definition files of one folder; scopes are the
// agents that share it. Every file of an agent among them is marked seen.
func (s *Service) scanBase(ctx context.Context, base string, scopes []Scope, seen map[string]bool) {
	files, err := definitionFiles(base)
	if err != nil {
		s.host.Logger.Warnf("classifiers in %s cannot be listed: %v", base, err)
		return
	}
	for _, path := range files {
		info, err := os.Stat(path)
		if err != nil {
			continue
		}
		key := entryKey(base, filepathStem(path))
		s.mu.Lock()
		e, known := s.entries[key]
		unchanged := known && e.modTime.Equal(info.ModTime()) && e.size == info.Size()
		s.mu.Unlock()
		if unchanged {
			for _, sc := range scopes {
				if sameAgent(sc.AgentID, e.scope.AgentID) {
					seen[key] = true
				}
			}
			continue
		}
		fi := readFile(path, s.host.ceilings())
		var scope Scope
		for _, sc := range scopes {
			if sameAgent(fi.owner, sc.AgentID) {
				scope = sc
			}
		}
		if scope.AgentID == "" && fi.owner != "" {
			// Owned by an agent whose switch is off (or that shares the folder
			// without classifiers): not run, not reported here.
			s.mu.Lock()
			if known {
				e.stopRun()
				delete(s.entries, key)
			}
			s.mu.Unlock()
			continue
		}
		if scope.AgentID == "" {
			scope = scopes[0] // unreadable owner: reported in the folder's first scope
		}
		seen[key] = true
		s.mu.Lock()
		if !known {
			e = &entry{key: key, nextDue: s.host.clock().Now()}
			s.entries[key] = e
		}
		e.scope, e.modTime, e.size = scope, info.ModTime(), info.Size()
		e.def, e.invalid = fi.def, fi.invalid
		s.mu.Unlock()
		if fi.def == nil {
			s.host.Logger.Warnf("classifier file %s (agent %s) is not run: %s", path, scope.AgentID, fi.invalid)
			// An unreadable owner is the folder's agent when it is the only one.
			if sameAgent(fi.owner, scope.AgentID) || (fi.owner == "" && len(scopes) == 1) {
				s.reportInvalid(ctx, scope, path, fi)
			}
			continue
		}
		if !known {
			if st, err := s.state(ctx, base); err == nil {
				s.prune(ctx, st, fi.def, false)
			}
		}
		s.validAgain(ctx, scope, fi.def.ID)
	}
}

// reportInvalid tells the owning agent that a definition file it read when
// the file changed (a hand edit, or a change of the install's maximums
// found at the next change) is not valid and is not run, as a failed run
// is told: a run_failed activity row and the notices, counted and muted
// alike (afterRun).
func (s *Service) reportInvalid(ctx context.Context, scope Scope, path string, fi fileInfo) {
	def := lenientDefinition(fi.raw, filepathStem(path))
	st, err := s.state(ctx, scope.BaseDirectory)
	if err != nil {
		if ctx.Err() == nil && !errors.Is(err, ErrClosed) {
			s.host.Logger.Errorf("classifier=%s agent=%s: open state: %v", def.ID, scope.AgentID, err)
		}
		return
	}
	// The same broken content is reported once, whatever restarts or
	// re-reads follow; the record is dropped once the file is valid.
	sum := sha256.Sum256(fi.raw)
	fresh, err := st.markInvalidReported(context.WithoutCancel(ctx), NormalizeID(def.ID), hex.EncodeToString(sum[:]))
	if err != nil {
		s.host.Logger.Errorf("classifier=%s agent=%s: store invalid report: %v", def.ID, scope.AgentID, err)
	}
	if !fresh && err == nil {
		return
	}
	text := s.host.redact(lowerFirst(fi.invalid))
	rec := RunRecord{
		Status: RunError, Error: text, DryRun: def.DryRun,
		cause: cause{kind: causeInvalidFile, summary: text},
	}
	s.afterRun(ctx, scope, def, st, rec, false)
}

// validAgain forgets that a definition file was reported as not valid, now
// that it is valid (reportInvalid).
func (s *Service) validAgain(ctx context.Context, scope Scope, id string) {
	st, err := s.state(ctx, scope.BaseDirectory)
	if err != nil {
		return // reported by the run
	}
	if err := st.clearInvalidReported(context.WithoutCancel(ctx), NormalizeID(id)); err != nil {
		s.host.Logger.Errorf("classifier=%s agent=%s: clear invalid report: %v", id, scope.AgentID, err)
	}
}

// lenientDefinition is what can be read of a definition file that is not
// valid, for its notices (report, dry_run); its id is the file's.
func lenientDefinition(raw []byte, id string) *Definition {
	var def Definition
	if err := json.Unmarshal(raw, &def); err != nil {
		def = Definition{} // not JSON: every setting has its default
	}
	def.ID = id
	return &def
}

// lowerFirst lowers the first letter of a sentence starting with a
// capitalised word ("The definition ..."), for use after a colon.
func lowerFirst(s string) string {
	r, n := utf8.DecodeRuneInString(s)
	next, _ := utf8.DecodeRuneInString(s[n:])
	if unicode.IsUpper(r) && unicode.IsLower(next) {
		return string(unicode.ToLower(r)) + s[n:]
	}
	return s
}

func filepathStem(path string) string {
	base := filepath.Base(path)
	return base[:len(base)-len(filepath.Ext(base))]
}

// startLocked starts a run of e, unless a rerun holds the classifier's lock
// (then it runs right after). s.mu is held.
func (s *Service) startLocked(ctx context.Context, e *entry) {
	if s.closing {
		return
	}
	l, ok := s.locks[e.key]
	if !ok {
		l = make(chan struct{}, 1)
		s.locks[e.key] = l
	}
	select {
	case l <- struct{}{}:
	default:
		// A rerun holds it: try again at every tick until it is free.
		e.pending, e.nextDue = true, time.Time{}
		return
	}
	stop := make(chan struct{})
	e.running, e.pending, e.stop = true, false, stop
	scope, def := e.scope, e.def
	s.wg.Go(func() {
		s.runClassifier(ctx, scope, def, stop)
		<-l
		s.mu.Lock()
		e.running = false
		if e.stop == stop {
			e.stop = nil
		}
		again := e.pending
		if again {
			e.pending = false
			e.nextDue = time.Time{}
		}
		s.mu.Unlock()
		if again {
			s.wake()
		}
	})
}

// stopEntry stops the run in progress of the classifier with key.
func (s *Service) stopEntry(key string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e, ok := s.entries[key]; ok {
		e.stopRun()
	}
}
