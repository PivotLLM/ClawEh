// ClawEh
// License: MIT
//
// Copyright (c) 2026 Tenebris Technologies Inc.

package forum

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
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
// have already passed ValidID or are generated here.

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

	dirPerm  os.FileMode = 0o700
	filePerm os.FileMode = 0o600
)

// Cleanup marker names (<base>/.cleanup/<uuid>.<name>). Each is written
// before the work it names starts and removed when it is done, so a
// restart finishes it.
const (
	// CleanupAgents holds a JSON array of temporary agent IDs still to be
	// deleted after the forum reached a terminal state or was deleted.
	CleanupAgents = "agents.json"
)

// Store is one forum directory. It is safe for concurrent use by the
// goroutines of one controller; cross-process exclusion is Lock.
type Store struct {
	base string
	id   string
	root string
	lock *os.File
}

// CreateStore creates <base>/<forumID>/ with its subdirectories, and
// <base>/.locks/ and <base>/.cleanup/ if missing, and returns the store.
// It fails if the root already exists (a UUID collision is an error, never
// a reuse) or base is not an absolute path.
func CreateStore(base, forumID string) (*Store, error) {
	if !filepath.IsAbs(base) {
		return nil, fmt.Errorf("create forum store: base directory %q is not absolute", base)
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
	return &Store{base: base, id: forumID, root: root}, nil
}

// OpenStore opens an existing forum directory. It fails with ErrNotFound if
// the root or its forum.json is missing, and with ErrCorrupt wrapped
// around the detail if the layout is unusable. It does not verify
// contents; see Verify.
func OpenStore(base, forumID string) (*Store, error) {
	return nil, errNotImplemented
}

// ListForums returns the forum IDs under base: every directory whose name
// is a UUID and that contains forum.json, sorted. Dot-directories
// (.locks, .cleanup) are skipped. A missing base is an empty list, not an
// error.
func ListForums(base string) ([]string, error) {
	return nil, errNotImplemented
}

// ListStaged returns the forum IDs whose roots are staged for removal
// under <base>/.cleanup/ (a Remove interrupted by a crash); Recover
// finishes removing them with RemoveStaged.
func ListStaged(base string) ([]string, error) {
	return nil, errNotImplemented
}

// RemoveStaged removes a staged root <base>/.cleanup/<forumID>/ and that
// forum's markers.
func RemoveStaged(base, forumID string) error {
	return errNotImplemented
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

// cleanupPath is <base>/.cleanup/<id>.<name>.
func (s *Store) cleanupPath(name string) string {
	return filepath.Join(s.base, dirCleanup, s.id+"."+name)
}

// Lock takes the exclusive advisory lock on lockPath (flock, non-blocking)
// and writes the PID into it. It returns ErrLocked when another process
// holds it. Exactly one controller may run a forum at a time; Service
// holds the lock from Launch or Recover until the run pauses or ends.
// Locking a store this process already locked is a no-op.
func (s *Store) Lock() error {
	if s.lock != nil {
		return nil
	}
	_ = s.lockPath()
	return errNotImplemented
}

// Unlock releases the lock and removes the file. Unlocking an unlocked
// store is a no-op.
func (s *Store) Unlock() {
	if s.lock == nil {
		return
	}
	s.lock = nil
}

// WriteConfig writes forum.json with the bytes exactly as accepted; it
// fails if the file already exists (the configuration never changes after
// launch, §3.2).
func (s *Store) WriteConfig(raw []byte) error {
	return s.writeFileDurable(s.Path(fileConfig), raw, true)
}

// ReadConfig returns forum.json verbatim.
func (s *Store) ReadConfig() ([]byte, error) {
	return nil, errNotImplemented
}

// WriteSnapshot writes snapshot.json; it fails if the file already exists.
func (s *Store) WriteSnapshot(snap *Snapshot) error {
	return errNotImplemented
}

// ReadSnapshot reads snapshot.json.
func (s *Store) ReadSnapshot() (*Snapshot, error) {
	return nil, errNotImplemented
}

// WriteParticipants writes participants.json (§8: before the first
// dispatch). Rewriting it is allowed only before CommitLaunched exists;
// afterwards the caller must not change it.
func (s *Store) WriteParticipants(p *Participants) error {
	return errNotImplemented
}

// ReadParticipants reads participants.json; ErrNotFound if absent.
func (s *Store) ReadParticipants() (*Participants, error) {
	return nil, errNotImplemented
}

// WriteSource materialises one source as sources/<id><ext> and returns its
// record (root-relative path and digest). It fails if the file exists.
func (s *Store) WriteSource(id string, format Format, content []byte) (SourceRecord, error) {
	return SourceRecord{}, errNotImplemented
}

// WriteLayerInputs writes layers/<layer>/inputs.json. It fails if the file
// already exists: inputs are resolved once per layer and a resume reads
// them back (§4 random assignments are never reshuffled).
func (s *Store) WriteLayerInputs(in *LayerInputs) error {
	return errNotImplemented
}

// ReadLayerInputs reads layers/<layer>/inputs.json; ErrNotFound if absent.
func (s *Store) ReadLayerInputs(layerID string) (*LayerInputs, error) {
	return nil, errNotImplemented
}

// WriteAttemptRequest creates layers/<layer>/calls/<turn>/<attempt>/ and
// writes request.json. It fails if that attempt directory already exists:
// attempt numbers are allocated by the controller from the commits it
// replayed, and a collision means two controllers.
func (s *Store) WriteAttemptRequest(req *AttemptRequest) error {
	return errNotImplemented
}

// WriteAttemptReply writes reply.json beside the matching request.json. It
// fails if the request does not exist or a reply already does.
func (s *Store) WriteAttemptReply(layerID, turn string, attempt int, reply *AttemptReply) error {
	return errNotImplemented
}

// ListAttempts returns every attempt under a layer, ordered by turn ID and
// then attempt number, with Reply nil where reply.json is missing. A layer
// with no calls directory is an empty list. An attempt directory without a
// readable request.json is ErrCorrupt.
func (s *Store) ListAttempts(layerID string) ([]AttemptRecord, error) {
	return nil, errNotImplemented
}

// WriteOutput stores an accepted output in its attempt directory
// (layers/<layer>/calls/<turn>/<attempt>/output<ext>) and, when published
// is non-nil, the projection beside it (published<ext>); it fills
// out.ContentFile, out.PublishedFile (the same as ContentFile when
// published is nil) and out.Digest. It fails if an output already exists
// for that attempt.
func (s *Store) WriteOutput(out *OutputRecord, full, published []byte) error {
	return errNotImplemented
}

// ReadFile reads a file by its root-relative path (output content, a
// source, the transcript).
func (s *Store) ReadFile(rel string) ([]byte, error) {
	return nil, errNotImplemented
}

// AppendCommit assigns c.Seq = last seq + 1 and c.At = now, writes
// commits/<seq>.json durably, and returns the sequence number. It is
// serialised within the process; the file is created exclusively so a
// second writer fails rather than overwriting. The caller writes State
// afterwards (WriteState); a crash between the two is what Replay repairs.
func (s *Store) AppendCommit(c *Commit) (int, error) {
	return 0, errNotImplemented
}

// ReadCommits returns every commit in sequence order. A gap in the
// sequence or an unreadable file is ErrCorrupt; an empty log is an empty
// list.
func (s *Store) ReadCommits() ([]Commit, error) {
	return nil, errNotImplemented
}

// WriteState rewrites state.json. It is a cache; see Replay.
func (s *Store) WriteState(st *State) error {
	return errNotImplemented
}

// ReadState reads state.json; ErrNotFound if absent. Callers compare
// State.Seq with the last commit and replay when they differ.
func (s *Store) ReadState() (*State, error) {
	return nil, errNotImplemented
}

// WriteResult writes result.json once; it fails if the file exists.
func (s *Store) WriteResult(r *Result) error {
	return errNotImplemented
}

// ReadResult reads result.json; ErrNotFound while the forum is not
// terminal.
func (s *Store) ReadResult() (*Result, error) {
	return nil, errNotImplemented
}

// AppendTranscript appends text (which the caller terminates with a
// newline) to transcript.md and fsyncs. It is the only write to that file;
// callers pass only public material (§8).
func (s *Store) AppendTranscript(text string) error {
	return errNotImplemented
}

// SetCleanup writes the marker <base>/.cleanup/<id>.<name> with data.
func (s *Store) SetCleanup(name string, data []byte) error {
	return s.writeFileDurable(s.cleanupPath(name), data, false)
}

// Cleanup reads a marker; ok is false when it is absent.
func (s *Store) Cleanup(name string) (data []byte, ok bool, err error) {
	return nil, false, errNotImplemented
}

// ClearCleanup removes a marker; a missing marker is not an error.
func (s *Store) ClearCleanup(name string) error {
	return errNotImplemented
}

// Remove deletes the forum: it releases this store's own lock, fails with
// ErrLocked if another process holds the lock, renames the root into
// <base>/.cleanup/<id>/ (one atomic step that takes the forum out of
// ListForums), then removes the staged copy, the lock file and the
// forum's markers. A crash after the rename leaves a staged root that
// Recover removes with RemoveStaged.
func (s *Store) Remove() error {
	return errNotImplemented
}

// writeFileDurable writes data to path atomically: a temporary file in the
// same directory is written, fsynced and renamed over path, and the
// directory is fsynced so the rename itself is durable. With exclusive
// true it fails with os.ErrExist if path already exists (checked before
// the rename; two writers racing on the same path is a controller bug the
// run lock prevents).
func (s *Store) writeFileDurable(path string, data []byte, exclusive bool) error {
	if path == "" {
		return errors.New("write forum file: path escapes the forum root")
	}
	if exclusive {
		if _, err := os.Lstat(path); err == nil {
			return fmt.Errorf("write %s: %w", filepath.Base(path), os.ErrExist)
		}
	}
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return fmt.Errorf("write %s: %w", filepath.Base(path), err)
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
		return fmt.Errorf("write %s: %w", filepath.Base(path), err)
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
	if err := os.Rename(tmpPath, path); err != nil {
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
