// ClawEh
// License: MIT
//
// Copyright (c) 2026 Tenebris Technologies Inc.

package forum

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/PivotLLM/ClawEh/forum/forumfs"
)

// Reading and writing the forum's and its runs' files: the configuration
// and owner, the snapshot, participants, sources, layer inputs, attempts,
// outputs, state, result and transcript.

// WriteConfig writes the run's forum.json, the configuration the run
// uses (the forum's forum.json as Launch accepted it); it fails if the file
// already exists (a run's configuration never changes).
func (s *forumStore) WriteConfig(raw []byte) error {
	return s.writeRel(fileConfig, raw, true)
}

// ReadConfig returns the run's forum.json verbatim; ErrNotFound if absent.
func (s *forumStore) ReadConfig() ([]byte, error) {
	data, err := s.ReadFile(fileConfig)
	if err != nil {
		return nil, notFound(err)
	}
	return data, nil
}

// WriteForumConfig replaces the forum's current configuration
// (<dir>/forum.json) with raw, atomically.
func (s *forumStore) WriteForumConfig(raw []byte) error {
	err := s.inDir(func(r *os.Root) error { return writeFileAt(r, fileConfig, raw, false) })
	if err != nil {
		return fmt.Errorf("write the configuration of forum %s: %w", s.id, err)
	}
	return nil
}

// WriteForumMeta writes forum-meta.json once; it fails if the file exists.
func (s *forumStore) WriteForumMeta(m *ForumMeta) error {
	data, err := marshalRecord(m)
	if err != nil {
		return fmt.Errorf("write %s: %w", fileMeta, err)
	}
	if err := s.inDir(func(r *os.Root) error { return writeFileAt(r, fileMeta, data, true) }); err != nil {
		return fmt.Errorf("write the owner of forum %s: %w", s.id, err)
	}
	return nil
}

// ReadForumMeta reads forum-meta.json without following a symbolic link;
// ErrNotFound if absent, ErrCorrupt if it does not decode or names no
// owner.
func (s *forumStore) ReadForumMeta() (*ForumMeta, error) {
	var data []byte
	err := s.inDir(func(r *os.Root) error {
		if err := lstatRegular(r, fileMeta); err != nil {
			return err
		}
		var err error
		data, err = r.ReadFile(fileMeta)
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("read the owner of forum %s: %w", s.id, notFound(err))
	}
	var m ForumMeta
	if err := json.Unmarshal(data, &m); err != nil || m.Owner == "" {
		return nil, corrupt("%s does not name an owner", fileMeta)
	}
	return &m, nil
}

// ReadForumConfig returns the forum's current configuration
// (<dir>/forum.json) and when it was last written; ErrNotFound if absent.
// It is read without following a symbolic link.
func (s *forumStore) ReadForumConfig() ([]byte, time.Time, error) {
	var (
		data []byte
		mod  time.Time
	)
	err := s.inDir(func(r *os.Root) error {
		if err := lstatRegular(r, fileConfig); err != nil {
			return err
		}
		fi, err := r.Lstat(fileConfig)
		if err != nil {
			return err
		}
		mod = fi.ModTime().UTC()
		data, err = r.ReadFile(fileConfig)
		return err
	})
	if err != nil {
		return nil, time.Time{}, fmt.Errorf("read the configuration of forum %s: %w", s.id, notFound(err))
	}
	return data, mod, nil
}

// has reports whether the run-relative rel is a regular file.
func (s *forumStore) has(rel string) bool {
	return s.statRegular(rel) == nil
}

// WriteSnapshot writes snapshot.json; it fails if the file already exists.
func (s *forumStore) WriteSnapshot(snap *Snapshot) error {
	return s.writeJSON(fileSnapshot, snap, true)
}

// ReadSnapshot reads snapshot.json; ErrNotFound if absent.
func (s *forumStore) ReadSnapshot() (*Snapshot, error) {
	var snap Snapshot
	if err := s.readJSON(fileSnapshot, &snap); err != nil {
		return nil, err
	}
	return &snap, nil
}

// WriteParticipants writes participants.json (before the first
// dispatch). Rewriting it is allowed only while the commit log is empty
// (before CommitLaunched); afterwards it fails with ErrInvalidState.
func (s *forumStore) WriteParticipants(p *Participants) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.loadIndexLocked(); err != nil {
		return err
	}
	if s.idx.last > 0 {
		return fmt.Errorf("write %s: %w: the run is already launched", fileParticipants, ErrInvalidState)
	}
	return s.writeJSON(fileParticipants, p, false)
}

// ReadParticipants reads participants.json; ErrNotFound if absent.
func (s *forumStore) ReadParticipants() (*Participants, error) {
	var p Participants
	if err := s.readJSON(fileParticipants, &p); err != nil {
		return nil, err
	}
	return &p, nil
}

// WriteSource materialises one source as sources/<id><ext> and returns its
// record (root-relative path and digest). It fails if the file exists or
// id is not a configuration ID.
func (s *forumStore) WriteSource(id string, format Format, content []byte) (SourceRecord, error) {
	if !validID(id) {
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
// them back (random assignments are never reshuffled, DESIGN.md §5.6).
func (s *forumStore) WriteLayerInputs(in *LayerInputs) error {
	if !validID(in.LayerID) {
		return fmt.Errorf("write layer inputs: %q is not a layer ID", in.LayerID)
	}
	if err := s.inRoot(func(r *os.Root) error { return mkdirAt(r, path.Join(dirLayers, in.LayerID)) }); err != nil {
		return fmt.Errorf("write layer inputs: %w", err)
	}
	return s.writeJSON(path.Join(dirLayers, in.LayerID, fileInputs), in, true)
}

// ReadLayerInputs reads layers/<layer>/inputs.json; ErrNotFound if absent.
func (s *forumStore) ReadLayerInputs(layerID string) (*LayerInputs, error) {
	if !validID(layerID) {
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
	if !validID(layerID) || !validID(turn) || attempt < 1 {
		return ""
	}
	return path.Join(dirLayers, layerID, dirCalls, turn, strconv.Itoa(attempt))
}

// WriteAttemptRequest creates layers/<layer>/calls/<turn>/<attempt>/ and
// writes request.json. The directory is assembled under a temporary name
// and renamed into place, so an attempt directory always holds its
// request.
//
// An attempt already reserved (a CommitAttempt names it) is written again
// only as a resend (req.Resent) of the turn's newest attempt while it has
// no reply and was not resent before: a restart cut it, and request.json
// is rewritten in place.
// Otherwise a reserved attempt fails: attempt numbers are allocated by the
// controller from the reserved attempts, and a collision means two
// controllers. An attempt directory that exists without a reservation is
// the orphan of a crash between this write and its CommitAttempt; the
// message was never sent, so it is replaced.
func (s *forumStore) WriteAttemptRequest(req *AttemptRequest) error {
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
	if s.idx.reserved(req.Layer, req.Turn, req.Attempt) {
		return s.rewriteAttemptRequestLocked(req, rel, data)
	}
	err = s.inRoot(func(r *os.Root) error {
		turnDir := path.Dir(rel)
		if mkErr := mkdirAt(r, turnDir); mkErr != nil {
			return mkErr
		}
		tmp := path.Join(turnDir, forumfs.TempPrefix+"attempt-"+uuid.NewString())
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

// rewriteAttemptRequestLocked rewrites the request.json of a reserved
// attempt being resent (see WriteAttemptRequest). s.mu is held.
func (s *forumStore) rewriteAttemptRequestLocked(req *AttemptRequest, rel string, data []byte) error {
	if !req.Resent || req.Attempt != s.idx.latest[turnKey{req.Layer, req.Turn}] {
		return fmt.Errorf("write attempt request %s: %w", rel, os.ErrExist)
	}
	if s.idx.attempts[attemptKey{req.Layer, req.Turn, req.Attempt}].resent {
		return fmt.Errorf("write attempt request %s: %w: the attempt was already resent once", rel, os.ErrExist)
	}
	switch err := s.statRegular(path.Join(rel, fileReply)); {
	case err == nil:
		return fmt.Errorf("write attempt request %s: %w: the attempt has a reply", rel, os.ErrExist)
	case !errors.Is(err, fs.ErrNotExist):
		return fmt.Errorf("write attempt request %s: %w", rel, err)
	}
	if err := s.writeRel(path.Join(rel, fileRequest), data, false); err != nil {
		return fmt.Errorf("write attempt request: %w", err)
	}
	return nil
}

// WriteAttemptReply writes reply.json beside the matching request.json. It
// fails if the attempt is not reserved (no CommitAttempt: invariant 3
// orders the reservation before the Ask), the request does not exist, or
// a reply already does.
func (s *forumStore) WriteAttemptReply(layerID, turn string, attempt int, reply *AttemptReply) error {
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
func (s *forumStore) ListAttempts(layerID string) ([]AttemptRecord, error) {
	if !validID(layerID) {
		return nil, fmt.Errorf("list attempts: %q is not a layer ID", layerID)
	}
	s.mu.Lock()
	if err := s.loadIndexLocked(); err != nil {
		s.mu.Unlock()
		return nil, err
	}
	keys := make([]attemptKey, 0)
	resent := map[attemptKey]bool{}
	for k, r := range s.idx.attempts {
		if k.layer == layerID {
			keys = append(keys, k)
			resent[k] = r.resent
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
		rec := AttemptRecord{ResendUsed: resent[k]}
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
func (s *forumStore) WriteOutput(out *OutputRecord, full, published []byte) error {
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
func (s *forumStore) ReadFile(rel string) ([]byte, error) {
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
func (s *forumStore) ReadPrefix(rel string, keep int) (string, int, error) {
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
func (s *forumStore) statRegular(rel string) error {
	return s.inRoot(func(r *os.Root) error {
		clean, err := rootRel(rel)
		if err != nil {
			return err
		}
		return lstatRegular(r, clean)
	})
}

// WriteState rewrites state.json. It is a cache; see replay. Only the
// lock holder writes it: on a forumStore that does not hold the lock it fails
// with ErrInvalidState.
func (s *forumStore) WriteState(st *State) error {
	if !s.locked() {
		return fmt.Errorf("write %s: %w: the forum is not locked by this store", fileState, ErrInvalidState)
	}
	return s.writeJSON(fileState, st, false)
}

// ReadState reads state.json; ErrNotFound if absent. Callers compare
// State.Seq with the last commit and replay when they differ.
func (s *forumStore) ReadState() (*State, error) {
	var st State
	if err := s.readJSON(fileState, &st); err != nil {
		return nil, err
	}
	return &st, nil
}

// WriteResult writes result.json once; it fails if the file exists.
func (s *forumStore) WriteResult(r *Result) error {
	return s.writeJSON(fileResult, r, true)
}

// ReadResult reads result.json; ErrNotFound while the forum is not
// terminal.
func (s *forumStore) ReadResult() (*Result, error) {
	var r Result
	if err := s.readJSON(fileResult, &r); err != nil {
		return nil, err
	}
	return &r, nil
}

// AppendTranscript appends text (which the caller terminates with a
// newline) to transcript.md and fsyncs. With ReplaceTranscript it is the
// only write to that file; callers pass only public material. Writes
// are serialised, so entries never interleave.
func (s *forumStore) AppendTranscript(text string) error {
	if err := s.writeTranscript(os.O_APPEND, []byte(text)); err != nil {
		return fmt.Errorf("append transcript: %w", err)
	}
	return nil
}

// ReplaceTranscript rewrites transcript.md with data in place: the
// existing file is truncated, written and fsynced, never replaced by
// another file, so a reader following it (`tail -f`) keeps reading the
// same file. A crash part-way leaves a torn transcript, which the next
// openForum regenerates from the commit log (the transcript is derived). It is
// serialised with AppendTranscript.
func (s *forumStore) ReplaceTranscript(data []byte) error {
	if err := s.writeTranscript(os.O_TRUNC, data); err != nil {
		return fmt.Errorf("replace transcript: %w", err)
	}
	return nil
}

// writeTranscript opens transcript.md (refusing anything but a regular
// file reached without a symbolic link) with mode (os.O_APPEND or
// os.O_TRUNC), writes data and fsyncs, and fsyncs the directory when the
// file was created.
func (s *forumStore) writeTranscript(mode int, data []byte) error {
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

// writeJSON marshals v and writes it to the root-relative rel durably.
func (s *forumStore) writeJSON(rel string, v any, exclusive bool) error {
	data, err := marshalRecord(v)
	if err != nil {
		return fmt.Errorf("write %s: %w", rel, err)
	}
	return s.writeRel(rel, data, exclusive)
}

// readJSON reads the root-relative rel into v. A missing file is
// ErrNotFound and an undecodable one ErrCorrupt, both wrapped with rel.
func (s *forumStore) readJSON(rel string, v any) error {
	data, err := s.ReadFile(rel)
	if err != nil {
		return fmt.Errorf("read %s: %w", rel, notFound(err))
	}
	if err := json.Unmarshal(data, v); err != nil {
		return corrupt("%s: %v", rel, err)
	}
	return nil
}
