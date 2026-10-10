// ClawEh
// License: MIT
//
// Copyright (c) 2026 Tenebris Technologies Inc.

package forum

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// Cleanup markers (<base>/.cleanup/<uuid>.<run>.<name>) and removing a
// forum: staging its directory under .cleanup/ and finishing a removal a
// crash interrupted.

// Cleanup marker names (<base>/.cleanup/<uuid>.<name>). Each is written
// before the work it names starts and removed when it is done, so a
// restart finishes it.
const (
	// cleanupAgents holds a JSON array of temporary agent IDs still to be
	// deleted after the forum reached a terminal state or was deleted.
	cleanupAgents = "agents.json"
)

// cleanupPath is <base>/.cleanup/<id>.<run>.<name>, or "" for the forum
// handle (markers are per run) or a name that is empty or not a single
// path element.
func (s *forumStore) cleanupPath(name string) string {
	if s.run < 1 || name == "" || name == "." || name == ".." || strings.ContainsAny(name, `/\`) {
		return ""
	}
	return filepath.Join(s.base, dirCleanup, s.id+"."+strconv.Itoa(s.run)+"."+name)
}

// MarkedRuns returns the runs that have the cleanup marker name, in
// ascending order, whether or not their directories still exist.
func (s *forumStore) MarkedRuns(name string) ([]int, error) {
	pattern := filepath.Join(s.base, dirCleanup, s.id+".*."+name)
	matches, err := filepath.Glob(pattern)
	if err != nil {
		return nil, fmt.Errorf("list cleanup markers of forum %s: %w", s.id, err)
	}
	var runs []int
	for _, m := range matches {
		mid := strings.TrimSuffix(strings.TrimPrefix(filepath.Base(m), s.id+"."), "."+name)
		if !runDirPattern.MatchString(mid) {
			continue
		}
		n, err := strconv.Atoi(mid)
		if err == nil {
			runs = append(runs, n)
		}
	}
	sort.Ints(runs)
	return runs, nil
}

// listStaged returns the forum IDs whose roots are staged for removal
// under <base>/.cleanup/ (a Remove interrupted by a crash), sorted;
// Recover finishes removing them with removeStaged. A missing base or
// cleanup directory is an empty list.
func listStaged(base string) ([]string, error) {
	entries, err := os.ReadDir(filepath.Join(base, dirCleanup))
	if errors.Is(err, fs.ErrNotExist) {
		return []string{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("list staged forums: %w", err)
	}
	ids := []string{}
	for _, e := range entries {
		if e.IsDir() && validForumID(e.Name()) {
			ids = append(ids, e.Name())
		}
	}
	sort.Strings(ids)
	return ids, nil
}

// removeStaged removes a staged root <base>/.cleanup/<forumID>/, that
// forum's markers and its lock file. It takes the forum's lock first and
// fails with ErrLocked when another holder (a Remove still in progress)
// has it. Removing a forum with nothing staged is not an error.
func removeStaged(base, forumID string) error {
	if !validForumID(forumID) {
		return fmt.Errorf("remove staged forum: %q is not a forum ID", forumID)
	}
	s := newForumHandle(base, forumID)
	if err := s.Lock(); err != nil {
		return fmt.Errorf("remove staged forum %s: %w", forumID, err)
	}
	err := s.removeStagedAndMarkers()
	return errors.Join(err, s.unlock())
}

// removeStagedAndMarkers deletes <base>/.cleanup/<id>/ and every
// <base>/.cleanup/<id>.<name> marker.
func (s *forumStore) removeStagedAndMarkers() error {
	cleanup := filepath.Join(s.base, dirCleanup)
	if err := os.RemoveAll(filepath.Join(cleanup, s.id)); err != nil {
		return fmt.Errorf("remove staged forum %s: %w", s.id, err)
	}
	markers, err := filepath.Glob(filepath.Join(cleanup, s.id+".*"))
	if err != nil {
		return fmt.Errorf("remove markers of forum %s: %w", s.id, err)
	}
	for _, m := range markers {
		if err := os.Remove(m); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("remove markers of forum %s: %w", s.id, err)
		}
	}
	if err := syncDir(cleanup); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

// SetCleanup writes the marker <base>/.cleanup/<id>.<name> with data.
func (s *forumStore) SetCleanup(name string, data []byte) error {
	p := s.cleanupPath(name)
	if p == "" {
		return fmt.Errorf("write cleanup marker: bad name %q", name)
	}
	r, err := os.OpenRoot(filepath.Dir(p))
	if err != nil {
		return fmt.Errorf("write cleanup marker %s: %w", name, err)
	}
	err = writeFileAt(r, filepath.Base(p), data, false)
	if closeErr := r.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return fmt.Errorf("write cleanup marker %s: %w", name, err)
	}
	return nil
}

// Cleanup reads a marker; ok is false when it is absent.
func (s *forumStore) Cleanup(name string) (data []byte, ok bool, err error) {
	p := s.cleanupPath(name)
	if p == "" {
		return nil, false, fmt.Errorf("read cleanup marker: bad name %q", name)
	}
	data, err = os.ReadFile(p) //nolint:gosec // a marker under the forum base
	if errors.Is(err, fs.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("read cleanup marker %s: %w", name, err)
	}
	return data, true, nil
}

// ClearCleanup removes a marker; a missing marker is not an error.
func (s *forumStore) ClearCleanup(name string) error {
	p := s.cleanupPath(name)
	if p == "" {
		return fmt.Errorf("clear cleanup marker: bad name %q", name)
	}
	if err := os.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("clear cleanup marker %s: %w", name, err)
	}
	return syncDir(filepath.Dir(p))
}

// Remove deletes the forum, every run included: it takes the lock
// (keeping this store's own lock if it holds it), failing with ErrLocked
// if another holder has it, renames the forum directory into
// <base>/.cleanup/<id>/ (one atomic step that takes the forum out of
// listForums), then removes the staged copy and the markers of every run,
// and finally releases the lock and removes the lock file. A crash after the rename leaves a staged root that Recover
// removes with removeStaged.
func (s *forumStore) Remove() error {
	if err := s.Lock(); err != nil {
		return fmt.Errorf("remove forum %s: %w", s.id, err)
	}
	err := s.stageAndRemove()
	return errors.Join(err, s.unlock())
}

// stageAndRemove is Remove's work while the lock is held.
func (s *forumStore) stageAndRemove() error {
	cleanup := filepath.Join(s.base, dirCleanup)
	if err := os.MkdirAll(cleanup, dirPerm); err != nil {
		return fmt.Errorf("remove forum %s: %w", s.id, err)
	}
	staged := filepath.Join(cleanup, s.id)
	// A staged copy left by an earlier interrupted Remove of the same ID
	// would block the rename.
	if err := os.RemoveAll(staged); err != nil {
		return fmt.Errorf("remove forum %s: %w", s.id, err)
	}
	switch err := os.Rename(s.dir, staged); {
	case errors.Is(err, fs.ErrNotExist):
		// Already staged and removed; finish the markers.
	case err != nil:
		return fmt.Errorf("remove forum %s: %w", s.id, err)
	default:
		if err := syncDir(s.base); err != nil {
			return err
		}
	}
	return s.removeStagedAndMarkers()
}
