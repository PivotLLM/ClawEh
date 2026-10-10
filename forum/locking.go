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
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/PivotLLM/ClawEh/forum/forumfs"
)

// The forum's cross-process lock and the sweep of temporary entries a
// crashed writer left, which only the lock holder may do.

// forumLock is a forum's cross-process lock (an flock on lockPath), shared
// by every forumStore handle derived from one opening.
type forumLock struct {
	mu sync.Mutex
	f  *os.File
}

// errWouldBlock is tryLockFile's answer when another holder has the lock.
var errWouldBlock = errors.New("lock held by another holder")

// lockPath is <base>/.locks/<id>.run.
func (s *forumStore) lockPath() string {
	return filepath.Join(s.base, forumfs.LocksDir, s.id+lockSuffix)
}

// lockAttempts bounds how often Lock retries when the lock file is
// replaced under it (a holder removing it while releasing).
const lockAttempts = 5

// Lock takes the exclusive advisory lock on lockPath (flock, non-blocking)
// and writes the PID into it. Once it holds the lock it removes every
// temporary file or directory (forumfs.TempPrefix) a crashed writer left under the
// root (sweepTemp): no other writer can be mid-write while the lock is
// held. It returns ErrLocked when another process,
// or another forumStore of the same forum in this process, holds it. Exactly
// one controller may run a forum at a time; Service holds the lock from
// Launch or Recover until the run pauses or ends. Locking a store this
// forumStore already locked is a no-op.
//
// Unlock removes the lock file while still holding the lock, so after
// acquiring it Lock checks that the path still names the file it locked
// and starts again if not; without that check two holders could each
// lock a different inode.
func (s *forumStore) Lock() error {
	s.lk.mu.Lock()
	defer s.lk.mu.Unlock()
	if s.lk.f != nil {
		return nil
	}
	if err := os.MkdirAll(filepath.Join(s.base, forumfs.LocksDir), dirPerm); err != nil { //nolint:gosec // the lock directory under the forum base
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
		current, pathErr := os.Stat(p) //nolint:gosec // lock file under the forum base
		if statErr == nil && pathErr == nil && os.SameFile(held, current) {
			if err := writePID(f); err != nil {
				return fmt.Errorf("lock forum %s: %w", s.id, errors.Join(err, releaseFile(f)))
			}
			if err := s.sweepTemp(); err != nil {
				return fmt.Errorf("lock forum %s: %w", s.id, errors.Join(err, releaseFile(f)))
			}
			s.lk.f = f
			return nil
		}
		// The holder we waited behind removed the file; ours is stale.
		if err := releaseFile(f); err != nil {
			return fmt.Errorf("lock forum %s: %w", s.id, err)
		}
	}
	return fmt.Errorf("lock forum %s: the lock file kept changing", s.id)
}

// sweepTemp removes the temporary entries (forumfs.TempPrefix) a crashed writer
// left directly in the forum directory and in runs/ (a configuration
// write, a run being removed), and anywhere in this store's run when it is
// one (SweepRun). Earlier runs are not walked: nothing writes to them. A
// missing directory (a forum staged for removal) has nothing to sweep.
func (s *forumStore) sweepTemp() error {
	err := s.inDir(func(r *os.Root) error {
		for _, dir := range []string{".", dirRuns} {
			names, err := readDirNames(r, dir)
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			if err != nil {
				return err
			}
			for _, name := range names {
				if strings.HasPrefix(name, forumfs.TempPrefix) {
					if err := r.RemoveAll(path.Join(dir, name)); err != nil {
						return err
					}
				}
			}
		}
		return nil
	})
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("sweep temporary files: %w", err)
	}
	if s.run > 0 {
		return s.SweepRun()
	}
	return nil
}

// SweepRun removes every entry under the run's directory whose name starts
// with forumfs.TempPrefix (what a crash during a write left). Symbolic links are not
// followed. The caller holds the lock; openForum calls it before reading the run.
func (s *forumStore) SweepRun() error {
	var found []string
	err := filepath.WalkDir(s.root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if p == s.root && errors.Is(err, fs.ErrNotExist) {
				return filepath.SkipAll
			}
			return err
		}
		if p != s.root && strings.HasPrefix(d.Name(), forumfs.TempPrefix) {
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

// locked reports whether this forumStore (or a handle sharing its lock) holds
// the forum's lock.
func (s *forumStore) locked() bool {
	s.lk.mu.Lock()
	defer s.lk.mu.Unlock()
	return s.lk.f != nil
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

// removeLockFile removes <base>/.locks/<id>.run through an os.Root of the
// lock directory.
func removeLockFile(base, id string) error {
	r, err := os.OpenRoot(filepath.Join(base, forumfs.LocksDir))
	if err != nil {
		return err
	}
	err = r.Remove(id + lockSuffix)
	return errors.Join(err, r.Close())
}

// Unlock releases the lock and removes the file. Unlocking an unlocked
// store is a no-op. Errors are not reported: the lock is released when
// the file is closed whatever else fails, and a leftover lock file is
// harmless (the next Lock reuses it).
func (s *forumStore) Unlock() {
	_ = s.unlock() //nolint:errcheck // Unlock reports no errors; see its comment
}

// unlock is Unlock with its errors. The file is removed while the lock is
// still held (see Lock).
func (s *forumStore) unlock() error {
	s.lk.mu.Lock()
	defer s.lk.mu.Unlock()
	if s.lk.f == nil {
		return nil
	}
	f := s.lk.f
	s.lk.f = nil
	var err error
	if rmErr := removeLockFile(s.base, s.id); rmErr != nil && !errors.Is(rmErr, fs.ErrNotExist) {
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
