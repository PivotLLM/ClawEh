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
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/google/uuid"

	"github.com/PivotLLM/ClawEh/forum/forumfs"
)

// The on-disk store (DESIGN.md §4). A forum is a directory
// under a base directory holding its current configuration and its runs;
// one forumStore is either the forum directory (run 0: the configuration, the
// lock, the list of runs, removal) or one run of it (everything below):
//
//	<base>/
//	  .locks/<forum-uuid>.run              exclusive advisory lock of the forum
//	  .cleanup/<forum-uuid>.<run>.<name>   markers for work a restart must finish, per run
//	  .cleanup/<forum-uuid>/               a forum directory staged for removal (Remove)
//	  <forum-uuid>/
//	    forum-meta.json       ForumMeta: the owner, written once before forum.json
//	    forum.json            the current configuration (indented), edited between runs
//	    runs/<n>/             run n (1, 2, 3, ...), never changed by a later run:
//	      forum.json            the configuration the run used
//	      snapshot.json         Snapshot
//	      participants.json     Participants
//	      sources/<id><ext>     materialised sources
//	      layers/<layer>/inputs.json                           LayerInputs
//	      layers/<layer>/calls/<turn>/<attempt>/request.json   AttemptRequest
//	      layers/<layer>/calls/<turn>/<attempt>/reply.json     AttemptReply
//	      layers/<layer>/calls/<turn>/<attempt>/output<ext>    the accepted output (valid attempts only)
//	      layers/<layer>/calls/<turn>/<attempt>/published<ext> its projection (JSON with share only)
//	      commits/<seq>.json    Commit, seq zero-padded to commitSeqWidth digits
//	      state.json            State (derived cache)
//	      result.json           Result (terminal only)
//	      transcript.md         public transcript, appended (rewritten in place only by openForum's repair)
//
// The lock is the forum's, shared by every forumStore handle derived from one
// opening (Run, CreateRun, OpenRun), so a run holds the same lock as the
// forum it belongs to. Locks and cleanup staging sit beside the forum
// directories, not inside them, so removing one never removes
// the lock protecting it. Every write
// of a whole file is atomic and durable (writeFileAt): an exclusive write
// links its temporary file to the target, so a racing second writer fails
// with os.ErrExist instead of replacing the first. transcript.md is the
// exception: it is appended to, or rewritten in place, and fsynced after
// each write, so a reader following it keeps the same file. Directories are created 0700 and files
// 0600, as everywhere under CLAW_HOME. Every read and write inside the root
// goes through os.Root and refuses a symbolic link anywhere on the path
// below the root (noSymlinks), so nothing is read from or written to
// outside it; IDs used in paths (layer, participant, turn) are checked
// with validID, forum IDs must be canonical UUIDs.
//
// This file holds the handles and the layout of forums and runs; the rest
// of the store is locking.go (the lock and the temporary-entry sweep),
// runfiles.go (reading and writing the records), commits.go (the commit
// log and its index), cleanup.go (markers and removal) and atomicfs.go
// (the confined, atomic file primitives).

// File and directory names inside the base directory.
const (
	fileConfig       = "forum.json"
	fileMeta         = "forum-meta.json"
	fileSnapshot     = "snapshot.json"
	fileParticipants = "participants.json"
	fileState        = "state.json"
	fileResult       = "result.json"
	fileTranscript   = "transcript.md"
	fileInputs       = "inputs.json"
	fileRequest      = "request.json"
	fileReply        = "reply.json"
	fileOutput       = "output"    // + Format.Extension()
	filePublished    = "published" // + Format.Extension()
	dirSources       = "sources"
	dirLayers        = "layers"
	dirCalls         = "calls"
	dirCommits       = "commits"
	dirRuns          = "runs"
	dirCleanup       = ".cleanup"
	lockSuffix       = ".run"
	commitSeqWidth   = 8

	dirPerm  os.FileMode = 0o700
	filePerm os.FileMode = 0o600
)

// forumStore is one forum directory (run 0) or one run of it. It is safe for
// concurrent use by the goroutines of one controller; cross-process
// exclusion is Lock.
type forumStore struct {
	base string
	id   string
	// dir is <base>/<id>, the forum directory.
	dir string
	// run is the run number, 0 for the forum directory itself.
	run int
	// root is the run's directory (<dir>/runs/<run>); empty for run 0, so a
	// run file is never read from or written to the forum directory.
	root string
	// owner is the agent whose scope the store was opened in (set by the
	// service); when set, verify refuses a snapshot naming another
	// launcher, because the directory lives in that agent's workspace and
	// is not trusted for whose forum it is.
	owner string
	lk    *forumLock

	mu  sync.Mutex // guards idx, cfg and snap, and serialises commits and transcript writes
	idx commitIndex
	// cfg and snap are forum.json and snapshot.json as AppendCommit reads
	// them for checkCommit; both are immutable once written.
	cfg  *Config
	snap *Snapshot
}

// newForumHandle is the run-0 forumStore of <base>/<id>, with a lock of its own.
func newForumHandle(base, id string) *forumStore {
	return &forumStore{base: base, id: id, dir: filepath.Join(base, id), lk: &forumLock{}}
}

// Run returns the handle of run n of the forum, sharing this handle's lock
// and owner. It does not touch the disk; see OpenRun and CreateRun.
func (s *forumStore) Run(n int) *forumStore {
	return &forumStore{
		base: s.base, id: s.id, dir: s.dir, run: n, owner: s.owner, lk: s.lk,
		root: filepath.Join(s.dir, dirRuns, strconv.Itoa(n)),
	}
}

// RunNumber is the run the store is a handle of, 0 for the forum directory.
func (s *forumStore) RunNumber() int { return s.run }

// validForumID reports whether id is a canonical (lower-case, hyphenated)
// UUID, the only form a forum root may have.
func validForumID(id string) bool {
	u, err := uuid.Parse(id)
	return err == nil && u.String() == id
}

// createStore creates <base>/<forumID>/ with its runs/ directory, and
// <base>/.locks/ and <base>/.cleanup/ if missing, and returns the forum's
// handle (run 0). It fails if the directory already exists (a UUID
// collision is an error, never a reuse), base is not an absolute path or
// forumID is not a canonical UUID.
func createStore(base, forumID string) (*forumStore, error) {
	if !filepath.IsAbs(base) {
		return nil, fmt.Errorf("create forum store: base directory %q is not absolute", base)
	}
	if !validForumID(forumID) {
		return nil, fmt.Errorf("create forum store: %q is not a forum ID", forumID)
	}
	for _, d := range []string{base, filepath.Join(base, forumfs.LocksDir), filepath.Join(base, dirCleanup)} {
		if err := os.MkdirAll(d, dirPerm); err != nil {
			return nil, fmt.Errorf("create forum store: %w", err)
		}
	}
	s := newForumHandle(base, forumID)
	if err := os.Mkdir(s.dir, dirPerm); err != nil {
		return nil, fmt.Errorf("create forum store: %w", err)
	}
	if err := os.Mkdir(filepath.Join(s.dir, dirRuns), dirPerm); err != nil {
		return nil, fmt.Errorf("create forum store: %w", err)
	}
	if err := syncDir(s.dir); err != nil {
		return nil, fmt.Errorf("create forum store: %w", err)
	}
	if err := syncDir(base); err != nil {
		return nil, fmt.Errorf("create forum store: %w", err)
	}
	return s, nil
}

// openStore opens an existing forum directory and returns its handle (run
// 0). It fails with ErrNotFound if forumID is not a forum ID or the
// directory, its forum.json or its forum-meta.json is missing, and with ErrCorrupt wrapped
// around the detail if the layout is unusable (the directory or forum.json
// has the wrong type, or runs/ is missing or not a directory). It does not
// verify contents; see verify.
func openStore(base, forumID string) (*forumStore, error) {
	if !filepath.IsAbs(base) {
		return nil, fmt.Errorf("open forum store: base directory %q is not absolute", base)
	}
	if !validForumID(forumID) {
		return nil, fmt.Errorf("%w: %q", ErrNotFound, forumID)
	}
	s := newForumHandle(base, forumID)
	fi, err := os.Lstat(s.dir)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil, fmt.Errorf("%w: %s", ErrNotFound, forumID)
	case err != nil:
		return nil, fmt.Errorf("open forum store: %w", err)
	case !fi.IsDir():
		return nil, fmt.Errorf("%w: forum %s: its directory is not a directory", ErrCorrupt, forumID)
	}
	fi, err = os.Lstat(filepath.Join(s.dir, fileConfig))
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil, fmt.Errorf("%w: %s", ErrNotFound, forumID)
	case err != nil:
		return nil, fmt.Errorf("open forum store: %w", err)
	case !fi.Mode().IsRegular():
		return nil, fmt.Errorf("%w: forum %s: %s is not a regular file", ErrCorrupt, forumID, fileConfig)
	}
	if !regularFile(filepath.Join(s.dir, fileMeta)) {
		// A forum without its owner record is a leftover (Recover removes
		// it), never anyone's forum.
		return nil, fmt.Errorf("%w: %s", ErrNotFound, forumID)
	}
	if fi, err = os.Lstat(filepath.Join(s.dir, dirRuns)); err != nil || !fi.IsDir() {
		return nil, fmt.Errorf("%w: forum %s: %s/ is missing or not a directory", ErrCorrupt, forumID, dirRuns)
	}
	return s, nil
}

// listForums returns the forum IDs under base: every directory whose name
// is a UUID and that contains forum-meta.json and forum.json, sorted. Dot-directories (.locks,
// .cleanup) are skipped. A missing base is an empty list, not an error. A
// directory without forum.json (a forum whose creation died before writing
// it) is not a forum and is not listed.
func listForums(base string) ([]string, error) {
	entries, err := os.ReadDir(base)
	if errors.Is(err, fs.ErrNotExist) {
		return []string{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("list forums: %w", err)
	}
	ids := []string{}
	for _, e := range entries {
		if e.IsDir() && validForumID(e.Name()) && complete(filepath.Join(base, e.Name())) {
			ids = append(ids, e.Name())
		}
	}
	sort.Strings(ids)
	return ids, nil
}

// complete reports whether a forum directory has both forum-meta.json and
// forum.json, which NewForum writes in that order.
func complete(dir string) bool {
	return regularFile(filepath.Join(dir, fileMeta)) && regularFile(filepath.Join(dir, fileConfig))
}

// regularFile reports whether p is a regular file (not followed if a link).
func regularFile(p string) bool {
	fi, err := os.Lstat(p)
	return err == nil && fi.Mode().IsRegular()
}

// listIncomplete returns the forum IDs under base whose directories lack
// forum-meta.json or forum.json (a forum whose creation died before it
// wrote both, or a leftover without an owner record), sorted. Recover
// removes them.
func listIncomplete(base string) ([]string, error) {
	entries, err := os.ReadDir(base)
	if errors.Is(err, fs.ErrNotExist) {
		return []string{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("list forums: %w", err)
	}
	ids := []string{}
	for _, e := range entries {
		if e.IsDir() && validForumID(e.Name()) && !complete(filepath.Join(base, e.Name())) {
			ids = append(ids, e.Name())
		}
	}
	return ids, nil
}

// runDirPattern is the name of a run directory: a positive decimal number
// without leading zeros.
var runDirPattern = regexp.MustCompile(`^[1-9][0-9]{0,8}$`)

// Runs returns the run numbers that have a directory under runs/, in
// ascending order, whether or not their launch finished (see HasSnapshot).
// Temporary entries and anything else that is not a run directory are
// skipped.
func (s *forumStore) Runs() ([]int, error) {
	entries, err := os.ReadDir(filepath.Join(s.dir, dirRuns))
	if err != nil {
		return nil, fmt.Errorf("list runs of forum %s: %w", s.id, err)
	}
	var runs []int
	for _, e := range entries {
		if !e.IsDir() || !runDirPattern.MatchString(e.Name()) {
			continue
		}
		n, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		runs = append(runs, n)
	}
	sort.Ints(runs)
	return runs, nil
}

// HasSnapshot reports whether a run's launch got as far as its snapshot: a
// run without one never started and is undone (RemoveRun).
func (s *forumStore) HasSnapshot() bool {
	return s.root != "" && s.has(fileSnapshot)
}

// CreateRun creates run n's directory (runs/<n>/ with its subdirectories)
// and returns its handle, sharing this handle's lock. It fails if the
// directory exists. The caller holds the lock.
func (s *forumStore) CreateRun(n int) (*forumStore, error) {
	if n < 1 {
		return nil, fmt.Errorf("create run of forum %s: bad run number %d", s.id, n)
	}
	r := s.Run(n)
	err := s.inDir(func(root *os.Root) error {
		rel := path.Join(dirRuns, strconv.Itoa(n))
		if err := mkdirAt(root, dirRuns); err != nil {
			return err
		}
		if err := root.Mkdir(rel, dirPerm); err != nil {
			return err
		}
		for _, d := range []string{dirSources, dirLayers, dirCommits} {
			if err := root.Mkdir(path.Join(rel, d), dirPerm); err != nil {
				return err
			}
		}
		if err := syncDirAt(root, rel); err != nil {
			return err
		}
		return syncDirAt(root, dirRuns)
	})
	if err != nil {
		return nil, fmt.Errorf("create run %d of forum %s: %w", n, s.id, err)
	}
	return r, nil
}

// OpenRun returns the handle of an existing run, sharing this handle's
// lock: ErrNotFound when runs/<n>/ is missing, ErrCorrupt when it or a
// required subdirectory is not a directory.
func (s *forumStore) OpenRun(n int) (*forumStore, error) {
	if n < 1 {
		return nil, fmt.Errorf("%w: forum %s has no run %d", ErrNotFound, s.id, n)
	}
	r := s.Run(n)
	fi, err := os.Lstat(r.root)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil, fmt.Errorf("%w: forum %s has no run %d", ErrNotFound, s.id, n)
	case err != nil:
		return nil, fmt.Errorf("open run %d of forum %s: %w", n, s.id, err)
	case !fi.IsDir():
		return nil, fmt.Errorf("%w: forum %s: run %d is not a directory", ErrCorrupt, s.id, n)
	}
	for _, d := range []string{dirSources, dirLayers, dirCommits} {
		fi, err := os.Lstat(filepath.Join(r.root, d))
		if err != nil || !fi.IsDir() {
			return nil, fmt.Errorf("%w: forum %s run %d: %s/ is missing or not a directory", ErrCorrupt, s.id, n, d)
		}
	}
	return r, nil
}

// RemoveRun removes a run's directory: it is renamed to a temporary name
// under runs/ (one atomic step that takes the run out of Runs) and then
// deleted; a crash part-way leaves a temporary entry the next Lock sweeps.
// Its markers are not touched. The caller holds the lock.
func (s *forumStore) RemoveRun() error {
	if s.run < 1 {
		return fmt.Errorf("remove run of forum %s: not a run", s.id)
	}
	err := s.inDir(func(root *os.Root) error {
		rel := path.Join(dirRuns, strconv.Itoa(s.run))
		tmp := path.Join(dirRuns, forumfs.TempPrefix+"run-"+uuid.NewString())
		switch err := root.Rename(rel, tmp); {
		case errors.Is(err, fs.ErrNotExist):
			return nil
		case err != nil:
			return err
		}
		if err := syncDirAt(root, dirRuns); err != nil {
			return err
		}
		return root.RemoveAll(tmp)
	})
	if err != nil {
		return fmt.Errorf("remove run %d of forum %s: %w", s.run, s.id, err)
	}
	s.mu.Lock()
	s.idx = commitIndex{}
	s.cfg, s.snap = nil, nil
	s.mu.Unlock()
	return nil
}

// Root is the run directory's absolute path (empty for the forum handle).
func (s *forumStore) Root() string { return s.root }

// Dir is the forum directory's absolute path.
func (s *forumStore) Dir() string { return s.dir }

// ID is the forum's UUID.
func (s *forumStore) ID() string { return s.id }

// Path joins rel onto the root. rel must be a clean relative path that
// does not escape the root; anything else returns "" and the caller treats
// it as corrupt input.
func (s *forumStore) Path(rel string) string {
	clean := filepath.Clean(rel)
	if filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return ""
	}
	return filepath.Join(s.root, clean)
}
