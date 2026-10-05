// ClawEh
// License: MIT
//
// Copyright (c) 2026 Tenebris Technologies Inc.

package forum

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
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
	"syscall"
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
//	    forum.json            the configuration exactly as accepted (bytes verbatim)
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
//	    transcript.md         public transcript, append-only
//
// Locks and cleanup staging sit beside the roots, not inside them (rev 3
// §8), so removing a root never removes the lock protecting it. Every write
// of a whole file is atomic and durable (writeFileDurable). Appends
// (transcript.md) are fsynced after each write. Directories are created
// 0700 and files 0600, as everywhere under CLAW_HOME. Methods never follow
// a path outside the root; IDs used in paths (layer, participant, turn)
// are checked with ValidID, forum IDs must be canonical UUIDs.
//
// The store keeps an index of the commit log (last sequence number,
// reserved attempts, turns with a committed output). It is what lets the
// store tell a reserved attempt from an orphan request.json (a crash
// between WriteAttemptRequest and AppendCommit), and refuse a second
// output for one turn ID.

// File and directory names.
const (
	fileConfig       = "forum.json"
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

// commitIndex is what the store derives from the commit log for its own
// checks. It is loaded lazily and dropped (loaded false) whenever a commit
// write fails, so the next use re-reads the log from disk.
type commitIndex struct {
	loaded   bool
	last     int
	attempts map[attemptKey]bool
	outputs  map[turnKey]bool
}

// Store is one forum directory. It is safe for concurrent use by the
// goroutines of one controller; cross-process exclusion is Lock.
type Store struct {
	base string
	id   string
	root string

	mu   sync.Mutex // guards lock and idx, and serialises commits and transcript appends
	lock *os.File
	idx  commitIndex
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

// OpenStore opens an existing forum directory. It fails with ErrNotFound if
// forumID is not a forum ID or the root or its forum.json is missing, and
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
	fi, err = os.Lstat(filepath.Join(root, fileConfig))
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil, fmt.Errorf("%w: %s", ErrNotFound, forumID)
	case err != nil:
		return nil, fmt.Errorf("open forum store: %w", err)
	case !fi.Mode().IsRegular():
		return nil, fmt.Errorf("%w: forum %s: %s is not a regular file", ErrCorrupt, forumID, fileConfig)
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
// is a UUID and that contains forum.json, sorted. Dot-directories
// (.locks, .cleanup) are skipped. A missing base is an empty list, not an
// error. A root without forum.json (a launch that died before writing it)
// is not a forum and is not listed.
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
		fi, err := os.Lstat(filepath.Join(base, e.Name(), fileConfig))
		if err != nil || !fi.Mode().IsRegular() {
			continue
		}
		ids = append(ids, e.Name())
	}
	sort.Strings(ids)
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
// and writes the PID into it. It returns ErrLocked when another process,
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
		if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
			closeErr := f.Close()
			if errors.Is(err, syscall.EWOULDBLOCK) {
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

// writePID replaces the lock file's content with this process's PID.
func writePID(f *os.File) error {
	if err := f.Truncate(0); err != nil {
		return err
	}
	_, err := f.WriteAt([]byte(strconv.Itoa(os.Getpid())+"\n"), 0)
	return err
}

// releaseFile drops the flock on f and closes it.
func releaseFile(f *os.File) error {
	return errors.Join(syscall.Flock(int(f.Fd()), syscall.LOCK_UN), f.Close())
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

// WriteConfig writes forum.json with the bytes exactly as accepted; it
// fails if the file already exists (the configuration never changes after
// launch, §3.2).
func (s *Store) WriteConfig(raw []byte) error {
	return s.writeFileDurable(s.Path(fileConfig), raw, true)
}

// ReadConfig returns forum.json verbatim.
func (s *Store) ReadConfig() ([]byte, error) {
	data, err := os.ReadFile(s.Path(fileConfig))
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", fileConfig, notFound(err))
	}
	return data, nil
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
	if err := s.writeFileDurable(s.Path(rel), content, true); err != nil {
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
	dir := s.Path(path.Join(dirLayers, in.LayerID))
	if err := mkdirDurable(dir); err != nil {
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
	final := s.Path(rel)
	turnDir := filepath.Dir(final)
	if mkErr := mkdirDurable(turnDir); mkErr != nil {
		return fmt.Errorf("write attempt request: %w", mkErr)
	}
	tmp, err := os.MkdirTemp(turnDir, tmpPrefix+"attempt-*")
	if err != nil {
		return fmt.Errorf("write attempt request: %w", err)
	}
	if err := s.writeFileDurable(filepath.Join(tmp, fileRequest), data, true); err != nil {
		return errors.Join(err, os.RemoveAll(tmp))
	}
	if err := os.RemoveAll(final); err != nil { // an unreserved orphan, see above
		return fmt.Errorf("write attempt request: %w", errors.Join(err, os.RemoveAll(tmp)))
	}
	if err := os.Rename(tmp, final); err != nil {
		return fmt.Errorf("write attempt request: %w", errors.Join(err, os.RemoveAll(tmp)))
	}
	return syncDir(turnDir)
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
	if _, err := os.Stat(s.Path(path.Join(rel, fileRequest))); err != nil {
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
// published is nil) and out.Digest (SHA-256 of full).
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
	publishedRel := contentRel
	if err := s.writeFileDurable(s.Path(contentRel), full, false); err != nil {
		return err
	}
	if published != nil {
		publishedRel = path.Join(rel, filePublished+ext)
		if err := s.writeFileDurable(s.Path(publishedRel), published, false); err != nil {
			return err
		}
	}
	out.ContentFile = contentRel
	out.PublishedFile = publishedRel
	out.Digest = digest(full)
	return nil
}

// ReadFile reads a file by its root-relative path (output content, a
// source, the transcript). The read is confined to the root: a path or a
// symbolic link leading outside it fails.
func (s *Store) ReadFile(rel string) ([]byte, error) {
	if s.Path(rel) == "" {
		return nil, fmt.Errorf("read %q: path escapes the forum root", rel)
	}
	r, err := os.OpenRoot(s.root)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", rel, err)
	}
	data, err := r.ReadFile(filepath.Clean(rel))
	if closeErr := r.Close(); err == nil && closeErr != nil {
		err = closeErr
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", rel, err)
	}
	return data, nil
}

// AppendCommit assigns c.Seq = last seq + 1 and c.At = now, writes
// commits/<seq>.json durably, and returns the sequence number. It is
// serialised within the process; the file is created exclusively so a
// second writer fails rather than overwriting. The caller writes State
// afterwards (WriteState); a crash between the two is what Replay repairs.
//
// It enforces the log's invariants at the source: a CommitAttempt needs
// its request.json and must not repeat an attempt; a CommitTurn needs an
// Output naming the same layer and turn, a CommitModerated a Decision;
// and neither may give a turn ID a second output. On failure c.Seq and
// c.At are left as they were.
func (s *Store) AppendCommit(c *Commit) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.loadIndexLocked(); err != nil {
		return 0, err
	}
	if err := s.checkCommitLocked(c); err != nil {
		return 0, err
	}
	rec := *c
	rec.Seq = s.idx.last + 1
	rec.At = time.Now().UTC().Round(0)
	data, err := marshalRecord(&rec)
	if err != nil {
		return 0, fmt.Errorf("append commit: %w", err)
	}
	if err := s.writeFileDurable(s.Path(commitRel(rec.Seq)), data, true); err != nil {
		// The file may exist even though the write reported an error (a
		// failed directory sync); re-read the log before the next commit.
		s.idx.loaded = false
		return 0, fmt.Errorf("append commit: %w", err)
	}
	s.idx.add(&rec)
	c.Seq, c.At = rec.Seq, rec.At
	return rec.Seq, nil
}

// checkCommitLocked rejects a commit that would break the log's
// invariants (see AppendCommit).
func (s *Store) checkCommitLocked(c *Commit) error {
	if c.Kind == "" {
		return errors.New("append commit: no kind")
	}
	switch c.Kind {
	case CommitAttempt:
		rel := attemptRel(c.Layer, c.Turn, c.Attempt)
		if rel == "" {
			return fmt.Errorf("append commit: bad attempt %q/%q/%d", c.Layer, c.Turn, c.Attempt)
		}
		if s.idx.attempts[attemptKey{c.Layer, c.Turn, c.Attempt}] {
			return fmt.Errorf("append commit: attempt %s: %w", rel, os.ErrExist)
		}
		if _, err := os.Stat(s.Path(path.Join(rel, fileRequest))); err != nil {
			return fmt.Errorf("append commit: attempt %s has no request: %w", rel, err)
		}
	case CommitTurn:
		if c.Output == nil || c.Output.LayerID != c.Layer || c.Output.Turn != c.Turn {
			return fmt.Errorf("append commit: turn %q/%q needs an output of that layer and turn", c.Layer, c.Turn)
		}
		if s.idx.outputs[turnKey{c.Layer, c.Turn}] {
			return fmt.Errorf("append commit: turn %s/%s: %w", c.Layer, c.Turn, os.ErrExist)
		}
	case CommitModerated:
		if c.Decision == nil {
			return fmt.Errorf("append commit: moderation %q/%q has no decision", c.Layer, c.Turn)
		}
		if s.idx.outputs[turnKey{c.Layer, c.Turn}] {
			return fmt.Errorf("append commit: moderation %s/%s: %w", c.Layer, c.Turn, os.ErrExist)
		}
	case CommitLaunched, CommitLayerStarted, CommitRoundPublished, CommitLayerEnded,
		CommitPauseRequested, CommitPaused, CommitResumed, CommitCancelRequested, CommitEnded:
	default:
		return fmt.Errorf("append commit: unknown kind %q", c.Kind)
	}
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
	dir := s.Path(dirCommits)
	entries, err := os.ReadDir(dir)
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
		data, err := os.ReadFile(filepath.Join(dir, name)) //nolint:gosec // a commit file under the forum root
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
	s.idx = commitIndex{attempts: map[attemptKey]bool{}, outputs: map[turnKey]bool{}}
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
	case CommitTurn, CommitModerated:
		x.outputs[turnKey{c.Layer, c.Turn}] = true
	case CommitLaunched, CommitLayerStarted, CommitRoundPublished, CommitLayerEnded,
		CommitPauseRequested, CommitPaused, CommitResumed, CommitCancelRequested, CommitEnded:
	}
}

// WriteState rewrites state.json. It is a cache; see Replay.
func (s *Store) WriteState(st *State) error {
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
// newline) to transcript.md and fsyncs. It is the only write to that file;
// callers pass only public material (§8). Appends are serialised, so
// entries never interleave.
func (s *Store) AppendTranscript(text string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.Path(fileTranscript)
	_, statErr := os.Lstat(p)
	f, err := os.OpenFile(p, os.O_APPEND|os.O_CREATE|os.O_WRONLY, filePerm) //nolint:gosec // transcript under the forum root
	if err != nil {
		return fmt.Errorf("append transcript: %w", err)
	}
	if _, err := f.WriteString(text); err != nil {
		return fmt.Errorf("append transcript: %w", errors.Join(err, f.Close()))
	}
	if err := f.Sync(); err != nil {
		return fmt.Errorf("append transcript: %w", errors.Join(err, f.Close()))
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("append transcript: %w", err)
	}
	if errors.Is(statErr, fs.ErrNotExist) {
		return syncDir(s.root)
	}
	return nil
}

// SetCleanup writes the marker <base>/.cleanup/<id>.<name> with data.
func (s *Store) SetCleanup(name string, data []byte) error {
	return s.writeFileDurable(s.cleanupPath(name), data, false)
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
	return s.writeFileDurable(s.Path(rel), data, exclusive)
}

// readJSON reads the root-relative rel into v. A missing file is
// ErrNotFound and an undecodable one ErrCorrupt, both wrapped with rel.
func (s *Store) readJSON(rel string, v any) error {
	p := s.Path(rel)
	if p == "" {
		return fmt.Errorf("read %q: path escapes the forum root", rel)
	}
	data, err := os.ReadFile(p) //nolint:gosec // a record under the forum root
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
// newline-terminated, so the files read well by hand.
func marshalRecord(v any) ([]byte, error) {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}

// digest is the hex SHA-256 of data.
func digest(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// mkdirDurable creates dir (and missing parents, 0700) and fsyncs the
// parent of each directory it created.
func mkdirDurable(dir string) error {
	if dir == "" {
		return errors.New("path escapes the forum root")
	}
	if fi, err := os.Stat(dir); err == nil {
		if !fi.IsDir() {
			return fmt.Errorf("%s is not a directory", dir)
		}
		return nil
	}
	if err := mkdirDurable(filepath.Dir(dir)); err != nil {
		return err
	}
	if err := os.Mkdir(dir, dirPerm); err != nil && !errors.Is(err, fs.ErrExist) {
		return err
	}
	return syncDir(filepath.Dir(dir))
}

// writeFileDurable writes data to target atomically: a temporary file in
// the same directory is written, fsynced and renamed over target, and the
// directory is fsynced so the rename itself is durable. With exclusive
// true it fails with os.ErrExist if target already exists (checked before
// the rename; two writers racing on the same path is a controller bug the
// run lock prevents).
func (s *Store) writeFileDurable(target string, data []byte, exclusive bool) error {
	if target == "" {
		return errors.New("write forum file: path escapes the forum root")
	}
	if exclusive {
		if _, err := os.Lstat(target); err == nil {
			return fmt.Errorf("write %s: %w", filepath.Base(target), os.ErrExist)
		}
	}
	dir := filepath.Dir(target)
	tmp, err := os.CreateTemp(dir, tmpPrefix+"*")
	if err != nil {
		return fmt.Errorf("write %s: %w", filepath.Base(target), err)
	}
	tmpPath := tmp.Name()
	// abandon discards the temporary file after a failure, keeping every
	// error so nothing is silently dropped.
	abandon := func(err error, closeFirst bool) error {
		if closeFirst {
			err = errors.Join(err, tmp.Close())
		}
		if rmErr := os.Remove(tmpPath); rmErr != nil && !errors.Is(rmErr, os.ErrNotExist) {
			err = errors.Join(err, rmErr)
		}
		return fmt.Errorf("write %s: %w", filepath.Base(target), err)
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
	if err := os.Rename(tmpPath, target); err != nil {
		return abandon(err, false)
	}
	return syncDir(dir)
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
