package media

import (
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/PivotLLM/ClawEh/pkg/logger"
)

// MediaMeta holds metadata about a stored media file.
type MediaMeta struct {
	Filename    string
	ContentType string
	Source      string // "telegram", "discord", "tool:image-gen", etc.
}

// MediaStore manages the lifecycle of media files associated with processing scopes.
type MediaStore interface {
	// Store registers an existing local file under the given scope.
	// Returns a ref identifier (e.g. "media://<id>").
	// Store does not move or copy the file; it only records the mapping.
	Store(localPath string, meta MediaMeta, scope string) (ref string, err error)

	// Resolve returns the local file path for a given ref.
	Resolve(ref string) (localPath string, err error)

	// ResolveWithMeta returns the local file path and metadata for a given ref.
	ResolveWithMeta(ref string) (localPath string, meta MediaMeta, err error)

	// ReleaseAll deletes all files registered under the given scope
	// and removes the mapping entries. File-not-exist errors are ignored.
	ReleaseAll(scope string) error
}

// RefPinner is an optional MediaStore capability: pin refs so TTL cleanup skips
// them while at least one owner (a session key) holds the pin. Hosts recover it
// from a MediaStore via type assertion.
type RefPinner interface {
	// Pin marks ref as pinned by owner. Unknown refs return an error.
	Pin(ref, owner string) error
	// SetPins reconciles owner's pin set to exactly refs: new refs are pinned
	// (unknown ones skipped — they may have already expired), refs no longer
	// present are unpinned and become subject to normal TTL cleanup.
	SetPins(owner string, refs []string)
	// ReleasePins drops all pins held by owner.
	ReleasePins(owner string)
}

// mediaEntry holds the path and metadata for a stored media file.
type mediaEntry struct {
	path     string
	meta     MediaMeta
	storedAt time.Time
}

// MediaCleanerConfig configures the background TTL cleanup.
type MediaCleanerConfig struct {
	Enabled  bool
	MaxAge   time.Duration
	Interval time.Duration
}

// FileMediaStore is a pure in-memory implementation of MediaStore.
// Files are expected to already exist on disk (e.g. in the data-dir media
// staging directory, utils.MediaTempDir()).
type FileMediaStore struct {
	mu          sync.RWMutex
	refs        map[string]mediaEntry
	scopeToRefs map[string]map[string]struct{}
	refToScope  map[string]string
	// Pin bookkeeping (see RefPinner): a ref is exempt from CleanExpired while
	// at least one owner (session key) pins it. Lazily initialized.
	pinOwners map[string]map[string]struct{} // ref → owners
	ownerPins map[string]map[string]struct{} // owner → refs

	cleanerCfg MediaCleanerConfig
	stop       chan struct{}
	startOnce  sync.Once
	stopOnce   sync.Once
	nowFunc    func() time.Time // for testing
}

// NewFileMediaStore creates a new FileMediaStore without background cleanup.
func NewFileMediaStore() *FileMediaStore {
	return &FileMediaStore{
		refs:        make(map[string]mediaEntry),
		scopeToRefs: make(map[string]map[string]struct{}),
		refToScope:  make(map[string]string),
		nowFunc:     time.Now,
	}
}

// NewFileMediaStoreWithCleanup creates a FileMediaStore with TTL-based background cleanup.
func NewFileMediaStoreWithCleanup(cfg MediaCleanerConfig) *FileMediaStore {
	return &FileMediaStore{
		refs:        make(map[string]mediaEntry),
		scopeToRefs: make(map[string]map[string]struct{}),
		refToScope:  make(map[string]string),
		cleanerCfg:  cfg,
		stop:        make(chan struct{}),
		nowFunc:     time.Now,
	}
}

// Store registers a local file under the given scope. The file must exist.
func (s *FileMediaStore) Store(localPath string, meta MediaMeta, scope string) (string, error) {
	if _, err := os.Stat(localPath); err != nil {
		return "", fmt.Errorf("media store: %s: %w", localPath, err)
	}

	ref := "media://" + uuid.New().String()

	s.mu.Lock()
	defer s.mu.Unlock()

	s.refs[ref] = mediaEntry{path: localPath, meta: meta, storedAt: s.nowFunc()}
	if s.scopeToRefs[scope] == nil {
		s.scopeToRefs[scope] = make(map[string]struct{})
	}
	s.scopeToRefs[scope][ref] = struct{}{}
	s.refToScope[ref] = scope

	return ref, nil
}

// Resolve returns the local path for the given ref.
func (s *FileMediaStore) Resolve(ref string) (string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	entry, ok := s.refs[ref]
	if !ok {
		return "", fmt.Errorf("media store: unknown ref: %s", ref)
	}
	return entry.path, nil
}

// ResolveWithMeta returns the local path and metadata for the given ref.
func (s *FileMediaStore) ResolveWithMeta(ref string) (string, MediaMeta, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	entry, ok := s.refs[ref]
	if !ok {
		return "", MediaMeta{}, fmt.Errorf("media store: unknown ref: %s", ref)
	}
	return entry.path, entry.meta, nil
}

// ReleaseAll removes all files under the given scope and cleans up mappings.
// Phase 1 (under lock): remove entries from maps.
// Phase 2 (no lock): delete files from disk.
func (s *FileMediaStore) ReleaseAll(scope string) error {
	// Phase 1: collect paths and remove from maps under lock
	var paths []string

	s.mu.Lock()
	refs, ok := s.scopeToRefs[scope]
	if !ok {
		s.mu.Unlock()
		return nil
	}

	for ref := range refs {
		if entry, exists := s.refs[ref]; exists {
			paths = append(paths, entry.path)
		}
		delete(s.refs, ref)
		delete(s.refToScope, ref)
		for owner := range s.pinOwners[ref] {
			s.unpinLocked(ref, owner)
		}
	}
	delete(s.scopeToRefs, scope)
	s.mu.Unlock()

	// Phase 2: delete files without holding the lock
	for _, p := range paths {
		if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
			logger.WarnCF("media", "release: failed to remove file", map[string]any{
				"path":  p,
				"error": err.Error(),
			})
		}
	}

	return nil
}

// Compile-time check: FileMediaStore provides the optional pin capability.
var _ RefPinner = (*FileMediaStore)(nil)

// pinLocked records owner's pin on ref. Caller holds s.mu.
func (s *FileMediaStore) pinLocked(ref, owner string) {
	if s.pinOwners == nil {
		s.pinOwners = make(map[string]map[string]struct{})
	}
	if s.ownerPins == nil {
		s.ownerPins = make(map[string]map[string]struct{})
	}
	if s.pinOwners[ref] == nil {
		s.pinOwners[ref] = make(map[string]struct{})
	}
	s.pinOwners[ref][owner] = struct{}{}
	if s.ownerPins[owner] == nil {
		s.ownerPins[owner] = make(map[string]struct{})
	}
	s.ownerPins[owner][ref] = struct{}{}
}

// unpinLocked removes owner's pin on ref. Caller holds s.mu.
func (s *FileMediaStore) unpinLocked(ref, owner string) {
	if owners, ok := s.pinOwners[ref]; ok {
		delete(owners, owner)
		if len(owners) == 0 {
			delete(s.pinOwners, ref)
		}
	}
	if refs, ok := s.ownerPins[owner]; ok {
		delete(refs, ref)
		if len(refs) == 0 {
			delete(s.ownerPins, owner)
		}
	}
}

// Pin implements RefPinner. Unknown refs return an error so callers get loud
// feedback for typos/expired refs.
func (s *FileMediaStore) Pin(ref, owner string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.refs[ref]; !ok {
		return fmt.Errorf("media store: unknown ref: %s", ref)
	}
	s.pinLocked(ref, owner)
	return nil
}

// SetPins implements RefPinner: reconcile owner's pins to exactly refs.
// Unknown refs are skipped (they may have expired before pinning).
func (s *FileMediaStore) SetPins(owner string, refs []string) {
	want := make(map[string]struct{}, len(refs))
	for _, r := range refs {
		want[r] = struct{}{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for ref := range s.ownerPins[owner] {
		if _, keep := want[ref]; !keep {
			s.unpinLocked(ref, owner)
		}
	}
	for ref := range want {
		if _, ok := s.refs[ref]; ok {
			s.pinLocked(ref, owner)
		}
	}
}

// ReleasePins implements RefPinner: drop all pins held by owner. Unpinned
// entries past MaxAge are removed by the next cleanup sweep.
func (s *FileMediaStore) ReleasePins(owner string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for ref := range s.ownerPins[owner] {
		s.unpinLocked(ref, owner)
	}
}

// CleanExpired removes all entries older than MaxAge.
// Phase 1 (under lock): identify expired entries and remove from maps.
// Phase 2 (no lock): delete files from disk to minimize lock contention.
func (s *FileMediaStore) CleanExpired() int {
	if s.cleanerCfg.MaxAge <= 0 {
		return 0
	}

	// Phase 1: collect expired entries under lock
	type expiredEntry struct {
		ref  string
		path string
	}

	s.mu.Lock()
	cutoff := s.nowFunc().Add(-s.cleanerCfg.MaxAge)
	var expired []expiredEntry

	for ref, entry := range s.refs {
		if entry.storedAt.Before(cutoff) {
			// Pinned refs are referenced by a live session's history; skip them
			// until every owner releases the pin.
			if len(s.pinOwners[ref]) > 0 {
				continue
			}
			expired = append(expired, expiredEntry{ref: ref, path: entry.path})

			if scope, ok := s.refToScope[ref]; ok {
				if scopeRefs, ok := s.scopeToRefs[scope]; ok {
					delete(scopeRefs, ref)
					if len(scopeRefs) == 0 {
						delete(s.scopeToRefs, scope)
					}
				}
			}

			delete(s.refs, ref)
			delete(s.refToScope, ref)
		}
	}
	s.mu.Unlock()

	// Phase 2: delete files without holding the lock
	for _, e := range expired {
		if err := os.Remove(e.path); err != nil && !os.IsNotExist(err) {
			logger.WarnCF("media", "cleanup: failed to remove file", map[string]any{
				"path":  e.path,
				"error": err.Error(),
			})
		}
	}

	return len(expired)
}

// Start begins the background cleanup goroutine if cleanup is enabled.
// Safe to call multiple times; only the first call starts the goroutine.
func (s *FileMediaStore) Start() {
	if !s.cleanerCfg.Enabled || s.stop == nil {
		return
	}
	if s.cleanerCfg.Interval <= 0 || s.cleanerCfg.MaxAge <= 0 {
		logger.WarnCF("media", "cleanup: skipped due to invalid config", map[string]any{
			"interval": s.cleanerCfg.Interval.String(),
			"max_age":  s.cleanerCfg.MaxAge.String(),
		})
		return
	}

	s.startOnce.Do(func() {
		logger.InfoCF("media", "cleanup enabled", map[string]any{
			"interval": s.cleanerCfg.Interval.String(),
			"max_age":  s.cleanerCfg.MaxAge.String(),
		})

		go func() {
			ticker := time.NewTicker(s.cleanerCfg.Interval)
			defer ticker.Stop()

			for {
				select {
				case <-ticker.C:
					if n := s.CleanExpired(); n > 0 {
						logger.InfoCF("media", "cleanup: removed expired entries", map[string]any{
							"count": n,
						})
					}
				case <-s.stop:
					return
				}
			}
		}()
	})
}

// Stop terminates the background cleanup goroutine.
// Safe to call multiple times; only the first call closes the channel.
func (s *FileMediaStore) Stop() {
	if s.stop == nil {
		return
	}
	s.stopOnce.Do(func() {
		close(s.stop)
	})
}
