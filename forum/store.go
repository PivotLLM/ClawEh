// ClawEh
// License: MIT
//
// Copyright (c) 2026 Tenebris Technologies Inc.

package forum

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

// Seam (b): the on-disk store (spec §8, rev 3 §8). One Store is one forum
// under a base directory:
//
//	<base>/
//	  .locks/<forum-uuid>.run        exclusive advisory lock of the running controller
//	  .cleanup/<forum-uuid>.<name>   markers for work a restart must finish
//	  .cleanup/<forum-uuid>/         a forum root staged for removal (Remove)
//	  <forum-uuid>/
//	    forum.json            the configuration as launched (the draft's, indented)
//	    snapshot.json         Snapshot
//	    participants.json     Participants
//	    sources/<id><ext>     materialised sources
//	    layers/<layer>/inputs.json                           LayerInputs
//	    layers/<layer>/calls/<turn>/<attempt>/request.json   AttemptRequest
//	    layers/<layer>/calls/<turn>/<attempt>/reply.json     AttemptReply
//	    layers/<layer>/calls/<turn>/<attempt>/output<ext>    the accepted output (valid attempts only)
//	    layers/<layer>/calls/<turn>/<attempt>/published<ext> its projection (JSON with share only)
//	    commits/<seq>.json    Commit, seq zero-padded to commitSeqWidth digits
//	    state.json            State (derived cache)
//	    result.json           Result (terminal only)
//	    transcript.md         public transcript, appended (rewritten in place only by Open's repair)
//
// Locks and cleanup staging sit beside the roots, not inside them (rev 3
// §8), so removing a root never removes the lock protecting it. Every write
// of a whole file is atomic and durable (writeFileAt): an exclusive write
// links its temporary file to the target, so a racing second writer fails
// with os.ErrExist instead of replacing the first. transcript.md is the
// exception: it is appended to, or rewritten in place, and fsynced after
// each write, so a reader following it keeps the same file. Directories are created 0700 and files
// 0600, as everywhere under CLAW_HOME. Every read and write inside the root
// goes through os.Root and refuses a symbolic link anywhere on the path
// below the root (noSymlinks), so nothing is read from or written to
// outside it; IDs used in paths (layer, participant, turn) are checked
// with ValidID, forum IDs must be canonical UUIDs.
//
// The store keeps an index of the commit log (last sequence number,
// reserved attempts, turns with a committed output). It is what lets the
// store tell a reserved attempt from an orphan request.json (a crash
// between WriteAttemptRequest and AppendCommit), and refuse a second
// output for one turn ID.

// File and directory names.
const (
	fileConfig       = "forum.json"
	fileDraft        = "draft.json"
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
	dirLocks         = ".locks"
	dirCleanup       = ".cleanup"
	lockSuffix       = ".run"
	commitSeqWidth   = 8

	// tmpPrefix starts every temporary file or directory the store
	// creates; readers skip such entries (they are what a crash leaves
	// behind and were never published by a rename).
	tmpPrefix = ".tmp-"

	dirPerm  os.FileMode = 0o700
	filePerm os.FileMode = 0o600
)

// commitNamePattern is the file name of one commit: commitSeqWidth digits.
var commitNamePattern = regexp.MustCompile(`^[0-9]{` + strconv.Itoa(commitSeqWidth) + `}\.json$`)

// Cleanup marker names (<base>/.cleanup/<uuid>.<name>). Each is written
// before the work it names starts and removed when it is done, so a
// restart finishes it.
const (
	// CleanupAgents holds a JSON array of temporary agent IDs still to be
	// deleted after the forum reached a terminal state or was deleted.
	CleanupAgents = "agents.json"
)

// attemptKey identifies one reserved attempt.
type attemptKey struct {
	layer   string
	turn    string
	attempt int
}

// turnKey identifies one turn (work) ID within a layer.
type turnKey struct {
	layer string
	turn  string
}

// commitIndex is what the commit log says about work IDs: the last
// sequence number, the reserved attempts, the turns with a reservation and
// those with a committed output, and each layer's last published round. The
// store keeps one for its own checks (loaded lazily and dropped, loaded
// false, whenever a commit write fails, so the next use re-reads the log
// from disk); Replay builds one as it folds, and both pass it to
// checkCommit.
type commitIndex struct {
	loaded    bool
	last      int
	attempts  map[attemptKey]bool
	reserved  map[turnKey]bool
	outputs   map[turnKey]bool
	published map[string]int
}

// newCommitIndex is the index of an empty log.
func newCommitIndex() *commitIndex {
	return &commitIndex{
		attempts:  map[attemptKey]bool{},
		reserved:  map[turnKey]bool{},
		outputs:   map[turnKey]bool{},
		published: map[string]int{},
	}
}

// Store is one forum directory. It is safe for concurrent use by the
// goroutines of one controller; cross-process exclusion is Lock.
type Store struct {
	base string
	id   string
	root string
	// owner is the agent whose scope the store was opened in (set by the
	// service); when set, Verify refuses a snapshot naming another
	// launcher, because the directory lives in that agent's workspace and
	// is not trusted for whose forum it is.
	owner string

	mu   sync.Mutex // guards lock, idx, cfg and snap, and serialises commits and transcript writes
	lock *os.File
	idx  commitIndex
	// cfg and snap are forum.json and snapshot.json as AppendCommit reads
	// them for checkCommit; both are immutable once written.
	cfg  *Config
	snap *Snapshot
}

// validForumID reports whether id is a canonical (lower-case, hyphenated)
// UUID, the only form a forum root may have.
func validForumID(id string) bool {
	u, err := uuid.Parse(id)
	return err == nil && u.String() == id
}

// CreateStore creates <base>/<forumID>/ with its subdirectories, and
// <base>/.locks/ and <base>/.cleanup/ if missing, and returns the store.
// It fails if the root already exists (a UUID collision is an error, never
// a reuse), base is not an absolute path or forumID is not a canonical
// UUID.
func CreateStore(base, forumID string) (*Store, error) {
	if !filepath.IsAbs(base) {
		return nil, fmt.Errorf("create forum store: base directory %q is not absolute", base)
	}
	if !validForumID(forumID) {
		return nil, fmt.Errorf("create forum store: %q is not a forum ID", forumID)
	}
	for _, d := range []string{base, filepath.Join(base, dirLocks), filepath.Join(base, dirCleanup)} {
		if err := os.MkdirAll(d, dirPerm); err != nil {
			return nil, fmt.Errorf("create forum store: %w", err)
		}
	}
	root := filepath.Join(base, forumID)
	if err := os.Mkdir(root, dirPerm); err != nil {
		return nil, fmt.Errorf("create forum store: %w", err)
	}
	for _, d := range []string{dirSources, dirLayers, dirCommits} {
		if err := os.Mkdir(filepath.Join(root, d), dirPerm); err != nil {
			return nil, fmt.Errorf("create forum store: %w", err)
		}
	}
	if err := syncDir(root); err != nil {
		return nil, fmt.Errorf("create forum store: %w", err)
	}
	if err := syncDir(base); err != nil {
		return nil, fmt.Errorf("create forum store: %w", err)
	}
	return &Store{base: base, id: forumID, root: root}, nil
}

// OpenStore opens an existing forum directory, a draft included. It fails
// with ErrNotFound if forumID is not a forum ID or the root, or both its
// forum.json and its draft.json, are missing, and
// with ErrCorrupt wrapped around the detail if the layout is unusable (the
// root, forum.json or a required subdirectory has the wrong type or a
// subdirectory is missing). It does not verify contents; see Verify.
func OpenStore(base, forumID string) (*Store, error) {
	if !filepath.IsAbs(base) {
		return nil, fmt.Errorf("open forum store: base directory %q is not absolute", base)
	}
	if !validForumID(forumID) {
		return nil, fmt.Errorf("%w: %q", ErrNotFound, forumID)
	}
	root := filepath.Join(base, forumID)
	fi, err := os.Lstat(root)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil, fmt.Errorf("%w: %s", ErrNotFound, forumID)
	case err != nil:
		return nil, fmt.Errorf("open forum store: %w", err)
	case !fi.IsDir():
		return nil, fmt.Errorf("%w: forum %s: root is not a directory", ErrCorrupt, forumID)
	}
	name := fileConfig
	fi, err = os.Lstat(filepath.Join(root, name))
	if errors.Is(err, fs.ErrNotExist) {
		name = fileDraft
		fi, err = os.Lstat(filepath.Join(root, name))
	}
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil, fmt.Errorf("%w: %s", ErrNotFound, forumID)
	case err != nil:
		return nil, fmt.Errorf("open forum store: %w", err)
	case !fi.Mode().IsRegular():
		return nil, fmt.Errorf("%w: forum %s: %s is not a regular file", ErrCorrupt, forumID, name)
	}
	if name == fileDraft {
		// A draft whose reset to draft was interrupted, or whose creation
		// was, may lack a subdirectory; it is recreated rather than the
		// draft reported damaged.
		for _, d := range []string{dirSources, dirLayers, dirCommits} {
			if err := os.Mkdir(filepath.Join(root, d), dirPerm); err != nil && !errors.Is(err, fs.ErrExist) {
				return nil, fmt.Errorf("open forum store: %w", err)
			}
		}
	}
	for _, d := range []string{dirSources, dirLayers, dirCommits} {
		fi, err := os.Lstat(filepath.Join(root, d))
		if err != nil || !fi.IsDir() {
			return nil, fmt.Errorf("%w: forum %s: %s/ is missing or not a directory", ErrCorrupt, forumID, d)
		}
	}
	return &Store{base: base, id: forumID, root: root}, nil
}

// ListForums returns the forum IDs under base: every directory whose name
// is a UUID and that contains forum.json or draft.json, sorted, drafts
// included. Dot-directories (.locks, .cleanup) are skipped. A missing base
// is an empty list, not an error. A root with neither file (a draft that
// died before writing it) is not a forum and is not listed.
func ListForums(base string) ([]string, error) {
	entries, err := os.ReadDir(base)
	if errors.Is(err, fs.ErrNotExist) {
		return []string{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("list forums: %w", err)
	}
	ids := []string{}
	for _, e := range entries {
		if !e.IsDir() || !validForumID(e.Name()) {
			continue
		}
		if regularFile(filepath.Join(base, e.Name(), fileConfig)) || regularFile(filepath.Join(base, e.Name(), fileDraft)) {
			ids = append(ids, e.Name())
		}
	}
	sort.Strings(ids)
	return ids, nil
}

// regularFile reports whether p is a regular file (not followed if a link).
func regularFile(p string) bool {
	fi, err := os.Lstat(p)
	return err == nil && fi.Mode().IsRegular()
}

// ListIncomplete returns the forum IDs under base whose roots hold neither
// forum.json nor draft.json (a draft whose creation died before it wrote
// draft.json), sorted. Recover removes them.
func ListIncomplete(base string) ([]string, error) {
	entries, err := os.ReadDir(base)
	if errors.Is(err, fs.ErrNotExist) {
		return []string{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("list forums: %w", err)
	}
	ids := []string{}
	for _, e := range entries {
		root := filepath.Join(base, e.Name())
		if e.IsDir() && validForumID(e.Name()) && !regularFile(filepath.Join(root, fileConfig)) && !regularFile(filepath.Join(root, fileDraft)) {
			ids = append(ids, e.Name())
		}
	}
	return ids, nil
}

// ListStaged returns the forum IDs whose roots are staged for removal
// under <base>/.cleanup/ (a Remove interrupted by a crash), sorted;
// Recover finishes removing them with RemoveStaged. A missing base or
// cleanup directory is an empty list.
func ListStaged(base string) ([]string, error) {
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

// RemoveStaged removes a staged root <base>/.cleanup/<forumID>/, that
// forum's markers and its lock file. It takes the forum's lock first and
// fails with ErrLocked when another holder (a Remove still in progress)
// has it. Removing a forum with nothing staged is not an error.
func RemoveStaged(base, forumID string) error {
	if !validForumID(forumID) {
		return fmt.Errorf("remove staged forum: %q is not a forum ID", forumID)
	}
	s := &Store{base: base, id: forumID, root: filepath.Join(base, forumID)}
	if err := s.Lock(); err != nil {
		return fmt.Errorf("remove staged forum %s: %w", forumID, err)
	}
	err := s.removeStagedAndMarkers()
	return errors.Join(err, s.unlock())
}

// removeStagedAndMarkers deletes <base>/.cleanup/<id>/ and every
// <base>/.cleanup/<id>.<name> marker.
func (s *Store) removeStagedAndMarkers() error {
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

// Root is the forum directory's absolute path.
func (s *Store) Root() string { return s.root }

// ID is the forum's UUID.
func (s *Store) ID() string { return s.id }

// Path joins rel onto the root. rel must be a clean relative path that
// does not escape the root; anything else returns "" and the caller treats
// it as corrupt input.
func (s *Store) Path(rel string) string {
	clean := filepath.Clean(rel)
	if filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return ""
	}
	return filepath.Join(s.root, clean)
}

// errWouldBlock is tryLockFile's answer when another holder has the lock.
var errWouldBlock = errors.New("lock held by another holder")

// lockPath is <base>/.locks/<id>.run.
func (s *Store) lockPath() string {
	return filepath.Join(s.base, dirLocks, s.id+lockSuffix)
}

// cleanupPath is <base>/.cleanup/<id>.<name>, or "" for a name that is
// empty or not a single path element.
func (s *Store) cleanupPath(name string) string {
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, `/\`) {
		return ""
	}
	return filepath.Join(s.base, dirCleanup, s.id+"."+name)
}

// lockAttempts bounds how often Lock retries when the lock file is
// replaced under it (a holder removing it while releasing).
const lockAttempts = 5

// Lock takes the exclusive advisory lock on lockPath (flock, non-blocking)
// and writes the PID into it. Once it holds the lock it removes every
// temporary file or directory (tmpPrefix) a crashed writer left under the
// root (sweepTemp): no other writer can be mid-write while the lock is
// held. It returns ErrLocked when another process,
// or another Store of the same forum in this process, holds it. Exactly
// one controller may run a forum at a time; Service holds the lock from
// Launch or Recover until the run pauses or ends. Locking a store this
// Store already locked is a no-op.
//
// Unlock removes the lock file while still holding the lock, so after
// acquiring it Lock checks that the path still names the file it locked
// and starts again if not; without that check two holders could each
// lock a different inode.
func (s *Store) Lock() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.lock != nil {
		return nil
	}
	if err := os.MkdirAll(filepath.Join(s.base, dirLocks), dirPerm); err != nil {
		return fmt.Errorf("lock forum %s: %w", s.id, err)
	}
	p := s.lockPath()
	for range lockAttempts {
		f, err := os.OpenFile(p, os.O_CREATE|os.O_RDWR, filePerm) //nolint:gosec // lock file under the forum base
		if err != nil {
			return fmt.Errorf("lock forum %s: %w", s.id, err)
		}
		if err := tryLockFile(f); err != nil {
			closeErr := f.Close()
			if errors.Is(err, errWouldBlock) {
				return fmt.Errorf("%w: forum %s", ErrLocked, s.id)
			}
			return fmt.Errorf("lock forum %s: %w", s.id, errors.Join(err, closeErr))
		}
		held, statErr := f.Stat()
		current, pathErr := os.Stat(p)
		if statErr == nil && pathErr == nil && os.SameFile(held, current) {
			if err := writePID(f); err != nil {
				return fmt.Errorf("lock forum %s: %w", s.id, errors.Join(err, releaseFile(f)))
			}
			if err := s.sweepTemp(); err != nil {
				return fmt.Errorf("lock forum %s: %w", s.id, errors.Join(err, releaseFile(f)))
			}
			s.lock = f
			return nil
		}
		// The holder we waited behind removed the file; ours is stale.
		if err := releaseFile(f); err != nil {
			return fmt.Errorf("lock forum %s: %w", s.id, err)
		}
	}
	return fmt.Errorf("lock forum %s: the lock file kept changing", s.id)
}

// sweepTemp removes every entry under the root whose name starts with
// tmpPrefix. A missing root (a forum staged for removal) has nothing to
// sweep. Symbolic links are not followed.
func (s *Store) sweepTemp() error {
	var found []string
	err := filepath.WalkDir(s.root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if p == s.root && errors.Is(err, fs.ErrNotExist) {
				return filepath.SkipAll
			}
			return err
		}
		if p != s.root && strings.HasPrefix(d.Name(), tmpPrefix) {
			rel, relErr := filepath.Rel(s.root, p)
			if relErr != nil {
				return relErr
			}
			found = append(found, rel)
			if d.IsDir() {
				return filepath.SkipDir
			}
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("sweep temporary files: %w", err)
	}
	if len(found) == 0 {
		return nil
	}
	return s.inRoot(func(r *os.Root) error {
		for _, rel := range found {
			if err := r.RemoveAll(rel); err != nil {
				return fmt.Errorf("sweep temporary files: %w", err)
			}
		}
		return nil
	})
}

// locked reports whether this Store holds the forum's lock.
func (s *Store) locked() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lock != nil
}

// writePID replaces the lock file's content with this process's PID.
func writePID(f *os.File) error {
	if err := f.Truncate(0); err != nil {
		return err
	}
	_, err := f.WriteAt([]byte(strconv.Itoa(os.Getpid())+"\n"), 0)
	return err
}

// releaseFile drops the lock on f and closes it.
func releaseFile(f *os.File) error {
	return errors.Join(unlockFile(f), f.Close())
}

// Unlock releases the lock and removes the file. Unlocking an unlocked
// store is a no-op. Errors are not reported: the lock is released when
// the file is closed whatever else fails, and a leftover lock file is
// harmless (the next Lock reuses it).
func (s *Store) Unlock() {
	_ = s.unlock() //nolint:errcheck // Unlock reports no errors; see its comment
}

// unlock is Unlock with its errors. The file is removed while the lock is
// still held (see Lock).
func (s *Store) unlock() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.lock == nil {
		return nil
	}
	f := s.lock
	s.lock = nil
	var err error
	if rmErr := os.Remove(f.Name()); rmErr != nil && !errors.Is(rmErr, fs.ErrNotExist) {
		err = rmErr
	}
	if relErr := releaseFile(f); relErr != nil {
		err = errors.Join(err, relErr)
	}
	if err != nil {
		return fmt.Errorf("unlock forum %s: %w", s.id, err)
	}
	return nil
}

// WriteConfig writes forum.json, the draft's configuration as Launch
// accepted it (indented); it fails if the file already exists (the
// configuration never changes after launch, §3.2).
func (s *Store) WriteConfig(raw []byte) error {
	return s.writeRel(fileConfig, raw, true)
}

// ReadConfig returns forum.json verbatim; ErrNotFound if absent.
func (s *Store) ReadConfig() ([]byte, error) {
	data, err := s.ReadFile(fileConfig)
	if err != nil {
		return nil, notFound(err)
	}
	return data, nil
}

// WriteDraft writes draft.json, replacing it. Only a draft (a forum with no
// snapshot.json) has one, until its launch has committed.
func (s *Store) WriteDraft(d *Draft) error {
	return s.writeJSON(fileDraft, d, false)
}

// ReadDraft reads draft.json; ErrNotFound if absent. A draft owned by
// another agent than the one whose scope the store was opened in is
// ErrCorrupt: like a snapshot, the directory is not trusted for whose it is.
func (s *Store) ReadDraft() (*Draft, error) {
	var d Draft
	if err := s.readJSON(fileDraft, &d); err != nil {
		return nil, err
	}
	if s.owner != "" && d.Owner != s.owner {
		return nil, corrupt("%s names owner %q, not %q", fileDraft, d.Owner, s.owner)
	}
	return &d, nil
}

// IsDraft reports whether the forum is a draft: it has draft.json and no
// snapshot.json (a launch writes the snapshot before it removes the draft).
func (s *Store) IsDraft() bool {
	return s.has(fileDraft) && !s.has(fileSnapshot)
}

// has reports whether the root-relative rel is a regular file.
func (s *Store) has(rel string) bool {
	return s.statRegular(rel) == nil
}

// ClearDraft removes draft.json once the forum has been launched; a missing
// file is not an error.
func (s *Store) ClearDraft() error {
	err := s.inRoot(func(r *os.Root) error {
		if err := r.Remove(fileDraft); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		return syncDirAt(r, ".")
	})
	if err != nil {
		return fmt.Errorf("remove %s: %w", fileDraft, err)
	}
	return nil
}

// resetHook, when set (tests only), runs after each removal ResetToDraft
// makes, naming what was removed; an error stops the reset there, as a
// crash would.
var resetHook func(removed string) error

// ResetToDraft undoes a launch that did not finish: it removes everything
// under the root but draft.json and empties the required subdirectories,
// so the forum is the draft it was. It is safe to interrupt at any step:
// snapshot.json goes first, so the folder reads as a draft from then on,
// then forum.json, so a recovery or a launch that finds it resets again;
// the subdirectories are emptied, never removed. The caller holds the lock
// and has deleted the temporary agents.
func (s *Store) ResetToDraft() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	err := s.inRoot(func(r *os.Root) error {
		removed := func(name string) error {
			if err := syncDirAt(r, path.Dir(name)); err != nil {
				return err
			}
			if resetHook != nil {
				return resetHook(name)
			}
			return nil
		}
		for _, name := range []string{fileSnapshot, fileConfig} {
			switch err := r.Remove(name); {
			case errors.Is(err, fs.ErrNotExist):
			case err != nil:
				return err
			default:
				if err := removed(name); err != nil {
					return err
				}
			}
		}
		names, err := readDirNames(r, ".")
		if err != nil {
			return err
		}
		for _, name := range names {
			if name == fileDraft {
				continue
			}
			if fi, err := r.Lstat(name); err == nil && fi.IsDir() && isRequiredDir(name) {
				if err := emptyDirAt(r, name, removed); err != nil {
					return err
				}
				continue
			}
			if err := r.RemoveAll(name); err != nil {
				return err
			}
			if err := removed(name); err != nil {
				return err
			}
		}
		for _, dir := range []string{dirSources, dirLayers, dirCommits} {
			if err := mkdirAt(r, dir); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("reset forum %s to its draft: %w", s.id, err)
	}
	s.idx = commitIndex{}
	s.cfg, s.snap = nil, nil
	return nil
}

// isRequiredDir reports whether name is one of the subdirectories every
// forum root has.
func isRequiredDir(name string) bool {
	return name == dirSources || name == dirLayers || name == dirCommits
}

// emptyDirAt removes every entry of the r-relative directory dir, calling
// removed after each.
func emptyDirAt(r *os.Root, dir string, removed func(string) error) error {
	names, err := readDirNames(r, dir)
	if err != nil {
		return err
	}
	for _, name := range names {
		rel := path.Join(dir, name)
		if err := r.RemoveAll(rel); err != nil {
			return err
		}
		if err := removed(rel); err != nil {
			return err
		}
	}
	return nil
}

// readDirNames lists the r-relative directory dir, sorted.
func readDirNames(r *os.Root, dir string) ([]string, error) {
	d, err := r.Open(dir)
	if err != nil {
		return nil, err
	}
	names, err := d.Readdirnames(-1)
	if closeErr := d.Close(); err == nil {
		err = closeErr
	}
	sort.Strings(names)
	return names, err
}

// WriteSnapshot writes snapshot.json; it fails if the file already exists.
func (s *Store) WriteSnapshot(snap *Snapshot) error {
	return s.writeJSON(fileSnapshot, snap, true)
}

// ReadSnapshot reads snapshot.json; ErrNotFound if absent.
func (s *Store) ReadSnapshot() (*Snapshot, error) {
	var snap Snapshot
	if err := s.readJSON(fileSnapshot, &snap); err != nil {
		return nil, err
	}
	return &snap, nil
}

// WriteParticipants writes participants.json (§8: before the first
// dispatch). Rewriting it is allowed only while the commit log is empty
// (before CommitLaunched); afterwards it fails with ErrInvalidState.
func (s *Store) WriteParticipants(p *Participants) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.loadIndexLocked(); err != nil {
		return err
	}
	if s.idx.last > 0 {
		return fmt.Errorf("write %s: %w: the forum is already launched", fileParticipants, ErrInvalidState)
	}
	return s.writeJSON(fileParticipants, p, false)
}

// ReadParticipants reads participants.json; ErrNotFound if absent.
func (s *Store) ReadParticipants() (*Participants, error) {
	var p Participants
	if err := s.readJSON(fileParticipants, &p); err != nil {
		return nil, err
	}
	return &p, nil
}

// WriteSource materialises one source as sources/<id><ext> and returns its
// record (root-relative path and digest). It fails if the file exists or
// id is not a configuration ID.
func (s *Store) WriteSource(id string, format Format, content []byte) (SourceRecord, error) {
	if !ValidID(id) {
		return SourceRecord{}, fmt.Errorf("write source: %q is not a source ID", id)
	}
	rel := path.Join(dirSources, id+format.Extension())
	if err := s.writeRel(rel, content, true); err != nil {
		return SourceRecord{}, err
	}
	return SourceRecord{Decode: format, File: rel, Digest: digest(content)}, nil
}

// WriteLayerInputs writes layers/<layer>/inputs.json. It fails if the file
// already exists: inputs are resolved once per layer and a resume reads
// them back (§4 random assignments are never reshuffled).
func (s *Store) WriteLayerInputs(in *LayerInputs) error {
	if !ValidID(in.LayerID) {
		return fmt.Errorf("write layer inputs: %q is not a layer ID", in.LayerID)
	}
	if err := s.inRoot(func(r *os.Root) error { return mkdirAt(r, path.Join(dirLayers, in.LayerID)) }); err != nil {
		return fmt.Errorf("write layer inputs: %w", err)
	}
	return s.writeJSON(path.Join(dirLayers, in.LayerID, fileInputs), in, true)
}

// ReadLayerInputs reads layers/<layer>/inputs.json; ErrNotFound if absent.
func (s *Store) ReadLayerInputs(layerID string) (*LayerInputs, error) {
	if !ValidID(layerID) {
		return nil, fmt.Errorf("read layer inputs: %q is not a layer ID", layerID)
	}
	var in LayerInputs
	if err := s.readJSON(path.Join(dirLayers, layerID, fileInputs), &in); err != nil {
		return nil, err
	}
	return &in, nil
}

// attemptRel is layers/<layer>/calls/<turn>/<attempt>, or "" when an ID
// is malformed or attempt is not positive.
func attemptRel(layerID, turn string, attempt int) string {
	if !ValidID(layerID) || !ValidID(turn) || attempt < 1 {
		return ""
	}
	return path.Join(dirLayers, layerID, dirCalls, turn, strconv.Itoa(attempt))
}

// WriteAttemptRequest creates layers/<layer>/calls/<turn>/<attempt>/ and
// writes request.json. The directory is assembled under a temporary name
// and renamed into place, so an attempt directory always holds its
// request.
//
// It fails if that attempt is already reserved (a CommitAttempt names it):
// attempt numbers are allocated by the controller from the reserved
// attempts, and a collision means two controllers. An attempt directory
// that exists without a reservation is the orphan of a crash between this
// write and its CommitAttempt; the message was never sent, so it is
// replaced.
func (s *Store) WriteAttemptRequest(req *AttemptRequest) error {
	rel := attemptRel(req.Layer, req.Turn, req.Attempt)
	if rel == "" {
		return fmt.Errorf("write attempt request: bad attempt %q/%q/%d", req.Layer, req.Turn, req.Attempt)
	}
	data, err := marshalRecord(req)
	if err != nil {
		return fmt.Errorf("write attempt request: %w", err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if loadErr := s.loadIndexLocked(); loadErr != nil {
		return loadErr
	}
	if s.idx.attempts[attemptKey{req.Layer, req.Turn, req.Attempt}] {
		return fmt.Errorf("write attempt request %s: %w", rel, os.ErrExist)
	}
	err = s.inRoot(func(r *os.Root) error {
		turnDir := path.Dir(rel)
		if mkErr := mkdirAt(r, turnDir); mkErr != nil {
			return mkErr
		}
		tmp := path.Join(turnDir, tmpPrefix+"attempt-"+uuid.NewString())
		if mkErr := r.Mkdir(tmp, dirPerm); mkErr != nil {
			return mkErr
		}
		// Each step's failure removes the temporary directory as well.
		for _, step := range []func() error{
			func() error { return writeFileAt(r, path.Join(tmp, fileRequest), data, true) },
			func() error { return r.RemoveAll(rel) }, // an unreserved orphan, see above
			func() error { return r.Rename(tmp, rel) },
		} {
			if stepErr := step(); stepErr != nil {
				return errors.Join(stepErr, r.RemoveAll(tmp))
			}
		}
		return syncDirAt(r, turnDir)
	})
	if err != nil {
		return fmt.Errorf("write attempt request: %w", err)
	}
	return nil
}

// WriteAttemptReply writes reply.json beside the matching request.json. It
// fails if the attempt is not reserved (no CommitAttempt: invariant 3
// orders the reservation before the Ask), the request does not exist, or
// a reply already does.
func (s *Store) WriteAttemptReply(layerID, turn string, attempt int, reply *AttemptReply) error {
	rel := attemptRel(layerID, turn, attempt)
	if rel == "" {
		return fmt.Errorf("write attempt reply: bad attempt %q/%q/%d", layerID, turn, attempt)
	}
	s.mu.Lock()
	reserved, err := s.attemptReservedLocked(layerID, turn, attempt)
	s.mu.Unlock()
	if err != nil {
		return err
	}
	if !reserved {
		return fmt.Errorf("write attempt reply %s: %w: the attempt is not reserved", rel, ErrInvalidState)
	}
	if err := s.statRegular(path.Join(rel, fileRequest)); err != nil {
		return fmt.Errorf("write attempt reply %s: %w", rel, err)
	}
	return s.writeJSON(path.Join(rel, fileReply), reply, true)
}

// ListAttempts returns every reserved attempt under a layer (one named by
// a CommitAttempt), ordered by turn ID and then attempt number, with Reply
// nil where reply.json is missing. A layer with no reserved attempt is an
// empty list. Attempt directories without a reservation (orphans of a
// crash before the CommitAttempt) are not listed. A reserved attempt
// without a readable request.json, or with an unreadable reply.json, is
// ErrCorrupt.
func (s *Store) ListAttempts(layerID string) ([]AttemptRecord, error) {
	if !ValidID(layerID) {
		return nil, fmt.Errorf("list attempts: %q is not a layer ID", layerID)
	}
	s.mu.Lock()
	if err := s.loadIndexLocked(); err != nil {
		s.mu.Unlock()
		return nil, err
	}
	keys := make([]attemptKey, 0)
	for k := range s.idx.attempts {
		if k.layer == layerID {
			keys = append(keys, k)
		}
	}
	s.mu.Unlock()
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].turn != keys[j].turn {
			return keys[i].turn < keys[j].turn
		}
		return keys[i].attempt < keys[j].attempt
	})
	out := make([]AttemptRecord, 0, len(keys))
	for _, k := range keys {
		rel := attemptRel(k.layer, k.turn, k.attempt)
		var rec AttemptRecord
		if err := s.readJSON(path.Join(rel, fileRequest), &rec.Request); err != nil {
			return nil, corrupt("reserved attempt %s: %v", rel, err)
		}
		if rec.Request.Layer != k.layer || rec.Request.Turn != k.turn || rec.Request.Attempt != k.attempt {
			return nil, fmt.Errorf("%w: reserved attempt %s: request.json names another attempt", ErrCorrupt, rel)
		}
		var reply AttemptReply
		switch err := s.readJSON(path.Join(rel, fileReply), &reply); {
		case err == nil:
			rec.Reply = &reply
		case !errors.Is(err, ErrNotFound):
			return nil, corrupt("reserved attempt %s: %v", rel, err)
		}
		out = append(out, rec)
	}
	return out, nil
}

// WriteOutput stores an accepted output in its attempt directory
// (layers/<layer>/calls/<turn>/<attempt>/output<ext>) and, when published
// is non-nil, the projection beside it (published<ext>); it fills
// out.ContentFile, out.PublishedFile (the same as ContentFile when
// published is nil), out.Digest (SHA-256 of full) and out.PublishedDigest
// (SHA-256 of what PublishedFile holds).
//
// It fails if the attempt is not reserved or the turn already has a
// committed output (one output per work ID). Files written for an attempt
// whose CommitTurn never landed (a crash between this write and the
// commit) are not history and are replaced, which is what lets the
// controller adopt that reply on resume.
func (s *Store) WriteOutput(out *OutputRecord, full, published []byte) error {
	rel := attemptRel(out.LayerID, out.Turn, out.Attempt)
	if rel == "" {
		return fmt.Errorf("write output: bad attempt %q/%q/%d", out.LayerID, out.Turn, out.Attempt)
	}
	s.mu.Lock()
	reserved, err := s.attemptReservedLocked(out.LayerID, out.Turn, out.Attempt)
	committed := s.idx.outputs[turnKey{out.LayerID, out.Turn}]
	s.mu.Unlock()
	switch {
	case err != nil:
		return err
	case !reserved:
		return fmt.Errorf("write output %s: %w: the attempt is not reserved", rel, ErrInvalidState)
	case committed:
		return fmt.Errorf("write output %s: %w: turn %s already has a committed output", rel, os.ErrExist, out.Turn)
	}
	ext := out.Format.Extension()
	contentRel := path.Join(rel, fileOutput+ext)
	publishedRel, publishedDigest := contentRel, digest(full)
	if err := s.writeRel(contentRel, full, false); err != nil {
		return err
	}
	if published != nil {
		publishedRel, publishedDigest = path.Join(rel, filePublished+ext), digest(published)
		if err := s.writeRel(publishedRel, published, false); err != nil {
			return err
		}
	}
	out.ContentFile = contentRel
	out.PublishedFile = publishedRel
	out.Digest = digest(full)
	out.PublishedDigest = publishedDigest
	return nil
}

// ReadFile reads a regular file by its root-relative path (output
// content, a source, the transcript, a record). The read is confined to
// the root and follows no symbolic link: a path leading outside the root,
// or one with a link anywhere below the root, fails.
func (s *Store) ReadFile(rel string) ([]byte, error) {
	var data []byte
	err := s.inRoot(func(r *os.Root) error {
		clean, err := rootRel(rel)
		if err != nil {
			return err
		}
		if err = lstatRegular(r, clean); err != nil {
			return err
		}
		data, err = r.ReadFile(clean)
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", rel, err)
	}
	return data, nil
}

// ReadPrefix reads the regular file at the root-relative rel under the
// same rules as ReadFile, but holds at most keep characters of it in
// memory: it returns those first keep characters and the file's total
// length in characters (Unicode code points; invalid bytes count one each).
func (s *Store) ReadPrefix(rel string, keep int) (string, int, error) {
	var (
		prefix strings.Builder
		chars  int
	)
	err := s.inRoot(func(r *os.Root) error {
		clean, err := rootRel(rel)
		if err != nil {
			return err
		}
		if err = lstatRegular(r, clean); err != nil {
			return err
		}
		f, err := r.Open(clean)
		if err != nil {
			return err
		}
		br := bufio.NewReader(f)
		for {
			ch, _, rerr := br.ReadRune()
			if rerr != nil {
				closeErr := f.Close()
				if errors.Is(rerr, io.EOF) {
					return closeErr
				}
				return errors.Join(rerr, closeErr)
			}
			if chars < keep {
				prefix.WriteRune(ch)
			}
			chars++
		}
	})
	if err != nil {
		return "", 0, fmt.Errorf("read %s: %w", rel, err)
	}
	return prefix.String(), chars, nil
}

// statRegular checks that the root-relative rel is a regular file reached
// without following a symbolic link.
func (s *Store) statRegular(rel string) error {
	return s.inRoot(func(r *os.Root) error {
		clean, err := rootRel(rel)
		if err != nil {
			return err
		}
		return lstatRegular(r, clean)
	})
}

// AppendCommit writes c as commits/<seq>.json durably, with c.Seq = seq
// and c.At = now. seq is the sequence number the caller expects the commit
// to take, the one after the last commit it knows of: a mismatch with the
// log on disk (a caller working from a stale state, another writer) is
// refused before anything is written, with ErrInvalidState. It is
// serialised within the process; the file is created exclusively so a
// second writer fails rather than overwriting. The caller writes State
// afterwards (WriteState); a crash between the two is what Replay repairs.
//
// It enforces the log's invariants at the source with checkCommit, the
// same check Replay applies (so the store never writes a log Replay
// refuses), plus that a CommitAttempt's request.json exists. That needs
// forum.json and snapshot.json, so a commit before WriteSnapshot fails
// with ErrInvalidState. On failure c.Seq and c.At are left as they were.
func (s *Store) AppendCommit(seq int, c *Commit) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.loadIndexLocked(); err != nil {
		return err
	}
	if next := s.idx.last + 1; seq != next {
		return fmt.Errorf("append commit: %w: %s expected at seq %d, but the log of forum %s is at %d",
			ErrInvalidState, c.Kind, seq, s.id, s.idx.last)
	}
	if err := s.checkCommitLocked(c); err != nil {
		return err
	}
	rec := *c
	rec.Seq = seq
	rec.At = time.Now().UTC().Round(0)
	data, err := marshalRecord(&rec)
	if err != nil {
		return fmt.Errorf("append commit: %w", err)
	}
	if err := s.writeRel(commitRel(rec.Seq), data, true); err != nil {
		// The file may exist even though the write reported an error (a
		// failed directory sync); re-read the log before the next commit.
		s.idx.loaded = false
		return fmt.Errorf("append commit: %w", err)
	}
	s.idx.add(&rec)
	c.Seq, c.At = rec.Seq, rec.At
	return nil
}

// checkCommitLocked rejects a commit that would break the log's
// invariants (see AppendCommit).
func (s *Store) checkCommitLocked(c *Commit) error {
	if err := s.contractLocked(); err != nil {
		return fmt.Errorf("append commit: %w", err)
	}
	if err := checkCommit(s.cfg, s.snap, &s.idx, c); err != nil {
		return fmt.Errorf("append commit: %w", err)
	}
	if c.Kind == CommitAttempt {
		rel := attemptRel(c.Layer, c.Turn, c.Attempt)
		if err := s.statRegular(path.Join(rel, fileRequest)); err != nil {
			return fmt.Errorf("append commit: attempt %s has no request: %w", rel, err)
		}
	}
	return nil
}

// contractLocked loads forum.json and snapshot.json for checkCommit once.
func (s *Store) contractLocked() error {
	if s.snap != nil {
		return nil
	}
	snap, err := s.ReadSnapshot()
	if errors.Is(err, ErrNotFound) {
		return fmt.Errorf("%w: %s is not written yet", ErrInvalidState, fileSnapshot)
	}
	if err != nil {
		return err
	}
	raw, err := s.ReadConfig()
	if err != nil {
		return err
	}
	cfg, err := Decode(raw)
	if err != nil {
		return corrupt("%s: %v", fileConfig, err)
	}
	s.cfg, s.snap = cfg, snap
	return nil
}

// commitRel is commits/<seq>.json.
func commitRel(seq int) string {
	return path.Join(dirCommits, fmt.Sprintf("%0*d.json", commitSeqWidth, seq))
}

// ReadCommits returns every commit in sequence order. A gap in the
// sequence, a file whose seq differs from its name, a stray file or an
// unreadable file is ErrCorrupt; an empty log is an empty list. Temporary
// files a crash left behind are ignored.
func (s *Store) ReadCommits() ([]Commit, error) {
	var entries []fs.DirEntry
	err := s.inRoot(func(r *os.Root) error {
		fi, err := r.Lstat(dirCommits)
		if err != nil {
			return err
		}
		if !fi.IsDir() {
			return fmt.Errorf("%s/ is not a directory", dirCommits)
		}
		d, err := r.Open(dirCommits)
		if err != nil {
			return err
		}
		entries, err = d.ReadDir(-1)
		if closeErr := d.Close(); err == nil {
			err = closeErr
		}
		sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
		return err
	})
	if err != nil {
		return nil, corrupt("read commits: %v", err)
	}
	commits := make([]Commit, 0, len(entries))
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, tmpPrefix) {
			continue
		}
		if !commitNamePattern.MatchString(name) || !e.Type().IsRegular() {
			return nil, fmt.Errorf("%w: unexpected entry commits/%s", ErrCorrupt, name)
		}
		seq, err := strconv.Atoi(strings.TrimSuffix(name, ".json"))
		if err != nil {
			return nil, corrupt("commits/%s: %v", name, err)
		}
		data, err := s.ReadFile(path.Join(dirCommits, name))
		if err != nil {
			return nil, corrupt("commits/%s: %v", name, err)
		}
		var c Commit
		if err := json.Unmarshal(data, &c); err != nil {
			return nil, corrupt("commits/%s: %v", name, err)
		}
		if c.Seq != seq {
			return nil, fmt.Errorf("%w: commits/%s holds seq %d", ErrCorrupt, name, c.Seq)
		}
		commits = append(commits, c)
	}
	// ReadDir sorts by name and names are fixed-width, so this is seq order.
	for i, c := range commits {
		if c.Seq != i+1 {
			return nil, fmt.Errorf("%w: commit log has a gap before seq %d", ErrCorrupt, c.Seq)
		}
	}
	return commits, nil
}

// loadIndexLocked builds the commit index from disk if it is not loaded.
func (s *Store) loadIndexLocked() error {
	if s.idx.loaded {
		return nil
	}
	commits, err := s.ReadCommits()
	if err != nil {
		return err
	}
	s.idx = *newCommitIndex()
	for i := range commits {
		s.idx.add(&commits[i])
	}
	s.idx.loaded = true
	return nil
}

// attemptReservedLocked reports whether a CommitAttempt names the attempt.
func (s *Store) attemptReservedLocked(layerID, turn string, attempt int) (bool, error) {
	if err := s.loadIndexLocked(); err != nil {
		return false, err
	}
	return s.idx.attempts[attemptKey{layerID, turn, attempt}], nil
}

// add records one commit in the index.
func (x *commitIndex) add(c *Commit) {
	x.last = c.Seq
	switch c.Kind {
	case CommitAttempt:
		x.attempts[attemptKey{c.Layer, c.Turn, c.Attempt}] = true
		x.reserved[turnKey{c.Layer, c.Turn}] = true
	case CommitTurn, CommitModerated:
		x.outputs[turnKey{c.Layer, c.Turn}] = true
	case CommitRoundPublished:
		x.published[c.Layer] = c.Round
	case CommitLaunched, CommitLayerStarted, CommitLayerEnded,
		CommitPauseRequested, CommitPaused, CommitResumed, CommitCancelRequested, CommitEnded:
	}
}

// WriteState rewrites state.json. It is a cache; see Replay. Only the
// lock holder writes it: on a Store that does not hold the lock it fails
// with ErrInvalidState.
func (s *Store) WriteState(st *State) error {
	if !s.locked() {
		return fmt.Errorf("write %s: %w: the forum is not locked by this store", fileState, ErrInvalidState)
	}
	return s.writeJSON(fileState, st, false)
}

// ReadState reads state.json; ErrNotFound if absent. Callers compare
// State.Seq with the last commit and replay when they differ.
func (s *Store) ReadState() (*State, error) {
	var st State
	if err := s.readJSON(fileState, &st); err != nil {
		return nil, err
	}
	return &st, nil
}

// WriteResult writes result.json once; it fails if the file exists.
func (s *Store) WriteResult(r *Result) error {
	return s.writeJSON(fileResult, r, true)
}

// ReadResult reads result.json; ErrNotFound while the forum is not
// terminal.
func (s *Store) ReadResult() (*Result, error) {
	var r Result
	if err := s.readJSON(fileResult, &r); err != nil {
		return nil, err
	}
	return &r, nil
}

// AppendTranscript appends text (which the caller terminates with a
// newline) to transcript.md and fsyncs. With ReplaceTranscript it is the
// only write to that file; callers pass only public material (§8). Writes
// are serialised, so entries never interleave.
func (s *Store) AppendTranscript(text string) error {
	if err := s.writeTranscript(os.O_APPEND, []byte(text)); err != nil {
		return fmt.Errorf("append transcript: %w", err)
	}
	return nil
}

// ReplaceTranscript rewrites transcript.md with data in place: the
// existing file is truncated, written and fsynced, never replaced by
// another file, so a reader following it (`tail -f`) keeps reading the
// same file. A crash part-way leaves a torn transcript, which the next
// Open regenerates from the commit log (the transcript is derived). It is
// serialised with AppendTranscript.
func (s *Store) ReplaceTranscript(data []byte) error {
	if err := s.writeTranscript(os.O_TRUNC, data); err != nil {
		return fmt.Errorf("replace transcript: %w", err)
	}
	return nil
}

// writeTranscript opens transcript.md (refusing anything but a regular
// file reached without a symbolic link) with mode (os.O_APPEND or
// os.O_TRUNC), writes data and fsyncs, and fsyncs the directory when the
// file was created.
func (s *Store) writeTranscript(mode int, data []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.inRoot(func(r *os.Root) error {
		statErr := lstatRegular(r, fileTranscript)
		if statErr != nil && !errors.Is(statErr, fs.ErrNotExist) {
			return statErr
		}
		f, err := r.OpenFile(fileTranscript, mode|os.O_CREATE|os.O_WRONLY, filePerm)
		if err != nil {
			return err
		}
		if _, err := f.Write(data); err != nil {
			return errors.Join(err, f.Close())
		}
		if err := f.Sync(); err != nil {
			return errors.Join(err, f.Close())
		}
		if err := f.Close(); err != nil {
			return err
		}
		if statErr != nil {
			return syncDirAt(r, ".")
		}
		return nil
	})
}

// SetCleanup writes the marker <base>/.cleanup/<id>.<name> with data.
func (s *Store) SetCleanup(name string, data []byte) error {
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
func (s *Store) Cleanup(name string) (data []byte, ok bool, err error) {
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
func (s *Store) ClearCleanup(name string) error {
	p := s.cleanupPath(name)
	if p == "" {
		return fmt.Errorf("clear cleanup marker: bad name %q", name)
	}
	if err := os.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("clear cleanup marker %s: %w", name, err)
	}
	return syncDir(filepath.Dir(p))
}

// Remove deletes the forum: it takes the lock (keeping this store's own
// lock if it holds it), failing with ErrLocked if another holder has it,
// renames the root into <base>/.cleanup/<id>/ (one atomic step that takes
// the forum out of ListForums), then removes the staged copy and the
// forum's markers, and finally releases the lock and removes the lock
// file. A crash after the rename leaves a staged root that Recover
// removes with RemoveStaged.
func (s *Store) Remove() error {
	if err := s.Lock(); err != nil {
		return fmt.Errorf("remove forum %s: %w", s.id, err)
	}
	err := s.stageAndRemove()
	return errors.Join(err, s.unlock())
}

// stageAndRemove is Remove's work while the lock is held.
func (s *Store) stageAndRemove() error {
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
	switch err := os.Rename(s.root, staged); {
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

// writeJSON marshals v and writes it to the root-relative rel durably.
func (s *Store) writeJSON(rel string, v any, exclusive bool) error {
	data, err := marshalRecord(v)
	if err != nil {
		return fmt.Errorf("write %s: %w", rel, err)
	}
	return s.writeRel(rel, data, exclusive)
}

// readJSON reads the root-relative rel into v. A missing file is
// ErrNotFound and an undecodable one ErrCorrupt, both wrapped with rel.
func (s *Store) readJSON(rel string, v any) error {
	data, err := s.ReadFile(rel)
	if err != nil {
		return fmt.Errorf("read %s: %w", rel, notFound(err))
	}
	if err := json.Unmarshal(data, v); err != nil {
		return corrupt("%s: %v", rel, err)
	}
	return nil
}

// notFound maps a missing file to ErrNotFound and keeps other errors.
func notFound(err error) error {
	if errors.Is(err, fs.ErrNotExist) {
		return ErrNotFound
	}
	return err
}

// marshalRecord is the JSON form every record is written in: indented,
// newline-terminated and without HTML escaping (a "<" stays "<"), so the
// files read well by hand.
func marshalRecord(v any) ([]byte, error) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

// marshalCompact is json.Marshal without HTML escaping.
func marshalCompact(v any) ([]byte, error) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(b.Bytes(), []byte("\n")), nil
}

// digest is the hex SHA-256 of data.
func digest(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// inRoot runs fn with the forum root opened as an os.Root, so nothing fn
// does can reach outside the root.
func (s *Store) inRoot(fn func(r *os.Root) error) error {
	r, err := os.OpenRoot(s.root)
	if err != nil {
		return err
	}
	err = fn(r)
	if closeErr := r.Close(); err == nil {
		err = closeErr
	}
	return err
}

// writeRel writes data to the root-relative rel (writeFileAt).
func (s *Store) writeRel(rel string, data []byte, exclusive bool) error {
	clean, err := rootRel(rel)
	if err != nil {
		return fmt.Errorf("write forum file: %w", err)
	}
	return s.inRoot(func(r *os.Root) error { return writeFileAt(r, clean, data, exclusive) })
}

// rootRel cleans a root-relative path, refusing one that is absolute or
// escapes the root.
func rootRel(rel string) (string, error) {
	clean := filepath.Clean(filepath.FromSlash(rel))
	if rel == "" || filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path %q escapes the forum root", rel)
	}
	return clean, nil
}

// noSymlinks checks that every existing directory on the way to rel (rel
// itself excluded) is a real directory, not a symbolic link. Missing
// components are not an error; the operation that needs them fails.
func noSymlinks(r *os.Root, rel string) error {
	dir := filepath.Dir(rel)
	if dir == "." {
		return nil
	}
	cur := ""
	for elem := range strings.SplitSeq(dir, string(filepath.Separator)) {
		cur = filepath.Join(cur, elem)
		fi, err := r.Lstat(cur)
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		if fi.Mode()&fs.ModeSymlink != 0 {
			return fmt.Errorf("%s is a symbolic link", cur)
		}
		if !fi.IsDir() {
			return fmt.Errorf("%s is not a directory", cur)
		}
	}
	return nil
}

// lstatRegular checks that rel, reached without a symbolic link, is a
// regular file. A missing file wraps fs.ErrNotExist.
func lstatRegular(r *os.Root, rel string) error {
	if err := noSymlinks(r, rel); err != nil {
		return err
	}
	fi, err := r.Lstat(rel)
	if err != nil {
		return err
	}
	if !fi.Mode().IsRegular() {
		return fmt.Errorf("%s is not a regular file", rel)
	}
	return nil
}

// mkdirAt creates the root-relative dir (and missing parents, 0700),
// fsyncing the parent of each directory it creates. An existing component
// that is a symbolic link or not a directory fails.
func mkdirAt(r *os.Root, dir string) error {
	clean, err := rootRel(dir)
	if err != nil {
		return err
	}
	if clean == "." {
		return nil
	}
	parent := "."
	for elem := range strings.SplitSeq(clean, string(filepath.Separator)) {
		cur := filepath.Join(parent, elem)
		fi, err := r.Lstat(cur)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			if err = r.Mkdir(cur, dirPerm); err != nil {
				return err
			}
			if err = syncDirAt(r, parent); err != nil {
				return err
			}
		case err != nil:
			return err
		case fi.Mode()&fs.ModeSymlink != 0:
			return fmt.Errorf("%s is a symbolic link", cur)
		case !fi.IsDir():
			return fmt.Errorf("%s is not a directory", cur)
		}
		parent = cur
	}
	return nil
}

// writeFileAt writes data to the r-relative target atomically and
// durably: a new temporary file (tmpPrefix, created exclusively) in the
// target's directory is written and fsynced, then published, and the
// directory is fsynced. Without exclusive the temporary file is renamed
// over target. With exclusive it is hard-linked to target, which fails
// with os.ErrExist if target exists (even when created by a racing
// writer an instant earlier), and then removed. The target's directory
// must exist and no directory on the way may be a symbolic link.
func writeFileAt(r *os.Root, target string, data []byte, exclusive bool) error {
	name := filepath.Base(target)
	if err := noSymlinks(r, target); err != nil {
		return fmt.Errorf("write %s: %w", name, err)
	}
	dir := filepath.Dir(target)
	tmpPath := filepath.Join(dir, tmpPrefix+uuid.NewString())
	tmp, err := r.OpenFile(tmpPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, filePerm)
	if err != nil {
		return fmt.Errorf("write %s: %w", name, err)
	}
	// abandon discards the temporary file after a failure, keeping every
	// error so nothing is silently dropped.
	abandon := func(err error, closeFirst bool) error {
		if closeFirst {
			err = errors.Join(err, tmp.Close())
		}
		if rmErr := r.Remove(tmpPath); rmErr != nil && !errors.Is(rmErr, fs.ErrNotExist) {
			err = errors.Join(err, rmErr)
		}
		return fmt.Errorf("write %s: %w", name, err)
	}
	if err := tmp.Chmod(filePerm); err != nil {
		return abandon(err, true)
	}
	if _, err := tmp.Write(data); err != nil {
		return abandon(err, true)
	}
	if err := tmp.Sync(); err != nil {
		return abandon(err, true)
	}
	if err := tmp.Close(); err != nil {
		return abandon(err, false)
	}
	if exclusive {
		if err := r.Link(tmpPath, target); err != nil {
			return abandon(err, false)
		}
		if err := r.Remove(tmpPath); err != nil {
			return fmt.Errorf("write %s: %w", name, err)
		}
	} else if err := r.Rename(tmpPath, target); err != nil {
		return abandon(err, false)
	}
	return syncDirAt(r, dir)
}

// syncDirAt fsyncs the r-relative directory dir.
func syncDirAt(r *os.Root, dir string) error {
	d, err := r.Open(dir)
	if err != nil {
		return fmt.Errorf("sync %s: %w", dir, err)
	}
	if err := d.Sync(); err != nil {
		return fmt.Errorf("sync %s: %w", dir, errors.Join(err, d.Close()))
	}
	return d.Close()
}

// syncDir fsyncs a directory so a rename or create inside it is durable.
func syncDir(dir string) error {
	d, err := os.Open(dir) //nolint:gosec // a directory under the forum base
	if err != nil {
		return fmt.Errorf("sync %s: %w", dir, err)
	}
	if err := d.Sync(); err != nil {
		return fmt.Errorf("sync %s: %w", dir, errors.Join(err, d.Close()))
	}
	return d.Close()
}
