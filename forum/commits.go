// ClawEh
// License: MIT
//
// Copyright (c) 2026 Tenebris Technologies Inc.

package forum

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/PivotLLM/ClawEh/forum/forumfs"
)

// The commit log (commits/<seq>.json) and its index.
//
// A run's store keeps an index of the commit log (last sequence number,
// reserved attempts, turns with a committed output). It is what lets the
// store tell a reserved attempt from an orphan request.json (a crash
// between WriteAttemptRequest and AppendCommit), and refuse a second
// output for one turn ID.

// commitNamePattern is the file name of one commit: commitSeqWidth digits.
var commitNamePattern = regexp.MustCompile(`^[0-9]{` + strconv.Itoa(commitSeqWidth) + `}\.json$`)

// attemptKey identifies one reserved attempt.
type attemptKey struct {
	layer   string
	turn    string
	attempt int
}

// reservation is what an attempt's first CommitAttempt fixed: a resend
// reserves the attempt again only with the same round and ThroughSeq.
// resent records that a second CommitAttempt did so; there is no third.
type reservation struct {
	round, through int
	resent         bool
}

// turnKey identifies one turn (work) ID within a layer.
type turnKey struct {
	layer string
	turn  string
}

// commitIndex is what the commit log says about work IDs: the last
// sequence number, the reserved attempts, each turn's newest reserved
// attempt and the turns with a committed output, and each layer's last published round. The
// store keeps one for its own checks (loaded lazily and dropped, loaded
// false, whenever a commit write fails, so the next use re-reads the log
// from disk); replay builds one as it folds, and both pass it to
// checkCommit.
type commitIndex struct {
	loaded    bool
	last      int
	attempts  map[attemptKey]reservation
	latest    map[turnKey]int
	outputs   map[turnKey]bool
	published map[string]int
}

// newCommitIndex is the index of an empty log.
func newCommitIndex() *commitIndex {
	return &commitIndex{
		attempts:  map[attemptKey]reservation{},
		latest:    map[turnKey]int{},
		outputs:   map[turnKey]bool{},
		published: map[string]int{},
	}
}

// AppendCommit writes c as commits/<seq>.json durably, with c.Seq = seq
// and c.At = now. seq is the sequence number the caller expects the commit
// to take, the one after the last commit it knows of: a mismatch with the
// log on disk (a caller working from a stale state, another writer) is
// refused before anything is written, with ErrInvalidState. It is
// serialised within the process; the file is created exclusively so a
// second writer fails rather than overwriting. The caller writes State
// afterwards (WriteState); a crash between the two is what replay repairs.
//
// It enforces the log's invariants at the source with checkCommit, the
// same check replay applies (so the store never writes a log replay
// refuses), plus that a CommitAttempt's request.json exists. That needs
// forum.json and snapshot.json, so a commit before WriteSnapshot fails
// with ErrInvalidState. On failure c.Seq and c.At are left as they were.
func (s *forumStore) AppendCommit(seq int, c *Commit) error {
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
func (s *forumStore) checkCommitLocked(c *Commit) error {
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
		if s.idx.reserved(c.Layer, c.Turn, c.Attempt) {
			if err := s.checkResend(rel); err != nil {
				return fmt.Errorf("append commit: %w", err)
			}
			if err := s.statRegular(path.Join(rel, fileReply)); err == nil {
				return fmt.Errorf("append commit: attempt %s: %w: it has a reply", rel, os.ErrExist)
			}
		}
	}
	return nil
}

// checkResend checks that the reserved attempt at rel, reserved again, is a
// resend: its request.json is marked Resent.
func (s *forumStore) checkResend(rel string) error {
	var req AttemptRequest
	if err := s.readJSON(path.Join(rel, fileRequest), &req); err != nil {
		return err
	}
	if !req.Resent {
		return fmt.Errorf("attempt %s: %w: reserved again without being resent", rel, os.ErrExist)
	}
	return nil
}

// contractLocked loads forum.json and snapshot.json for checkCommit once.
func (s *forumStore) contractLocked() error {
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
	cfg, err := decodeConfig(raw)
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
func (s *forumStore) ReadCommits() ([]Commit, error) {
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
		if strings.HasPrefix(name, forumfs.TempPrefix) {
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
func (s *forumStore) loadIndexLocked() error {
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
func (s *forumStore) attemptReservedLocked(layerID, turn string, attempt int) (bool, error) {
	if err := s.loadIndexLocked(); err != nil {
		return false, err
	}
	return s.idx.reserved(layerID, turn, attempt), nil
}

// reserved reports whether a CommitAttempt names the attempt.
func (x *commitIndex) reserved(layerID, turn string, attempt int) bool {
	_, ok := x.attempts[attemptKey{layerID, turn, attempt}]
	return ok
}

// add records one commit in the index.
func (x *commitIndex) add(c *Commit) {
	x.last = c.Seq
	switch c.Kind {
	case CommitAttempt:
		k := attemptKey{c.Layer, c.Turn, c.Attempt}
		if r, ok := x.attempts[k]; ok {
			r.resent = true
			x.attempts[k] = r
		} else {
			x.attempts[k] = reservation{round: c.Round, through: c.ThroughSeq}
		}
		x.latest[turnKey{c.Layer, c.Turn}] = max(x.latest[turnKey{c.Layer, c.Turn}], c.Attempt)
	case CommitTurn, CommitModerated:
		x.outputs[turnKey{c.Layer, c.Turn}] = true
	case CommitRoundPublished:
		x.published[c.Layer] = c.Round
	case CommitLaunched, CommitLayerStarted, CommitLayerEnded,
		CommitPauseRequested, CommitPaused, CommitResumed, CommitCancelRequested, CommitEnded:
	}
}
