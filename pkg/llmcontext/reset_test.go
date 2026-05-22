// ClawEh
// License: MIT

package llmcontext

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PivotLLM/ClawEh/pkg/memory"
	"github.com/PivotLLM/ClawEh/pkg/providers"
)

// newResetStore returns a mockStore pre-populated with a few messages and a
// summary for the given sessionKey, ready for Reset tests.
func newResetStore(sessionKey string) *mockStore {
	s := newMockStore()
	s.history[sessionKey] = []providers.Message{
		{Role: "user", Content: "hello"},
		{Role: "assistant", Content: "hi"},
	}
	s.summary[sessionKey] = `{"version":1,"state":{"goals":"some goal"}}`
	return s
}

// newResetManager returns a *Manager suitable for reset tests.
// opts are applied after the base options so callers can override.
func newResetManager(store *mockStore, sessionKey string, opts ...Option) *Manager {
	baseOpts := []Option{
		WithContextWindow(10000),
		WithNormalPercent(90), // high threshold — compression won't fire in these tests
		WithSafetyPercent(95),
		WithMessageThreshold(100),
	}
	baseOpts = append(baseOpts, opts...)
	cm := New(sessionKey, store, nil, nil, baseOpts...)
	return cm.(*Manager)
}

// TestReset_ClearsHistory verifies that after Reset, GetHistory returns nil/empty.
func TestReset_ClearsHistory(t *testing.T) {
	const key = "test-session"
	store := newResetStore(key)
	mgr := newResetManager(store, key)

	if err := mgr.Reset(context.Background()); err != nil {
		t.Fatalf("Reset returned error: %v", err)
	}

	history := store.GetHistory(key)
	if len(history) != 0 {
		t.Errorf("expected empty history after Reset; got %d messages", len(history))
	}
}

// TestReset_ClearsSummary verifies that after Reset, GetSummary returns "".
func TestReset_ClearsSummary(t *testing.T) {
	const key = "test-session"
	store := newResetStore(key)
	mgr := newResetManager(store, key)

	// Confirm summary is non-empty before reset.
	if store.GetSummary(key) == "" {
		t.Fatal("precondition: summary should be non-empty before Reset")
	}

	if err := mgr.Reset(context.Background()); err != nil {
		t.Fatalf("Reset returned error: %v", err)
	}

	if got := store.GetSummary(key); got != "" {
		t.Errorf("expected empty summary after Reset; got %q", got)
	}
}

// TestReset_ClearsMsgCount verifies that after Reset, the in-memory msgCount is 0.
func TestReset_ClearsMsgCount(t *testing.T) {
	const key = "test-session"
	store := newResetStore(key)
	mgr := newResetManager(store, key)

	// Simulate some message activity.
	mgr.msgCount = 7
	mgr.compressedAtCount = 3
	mgr.cooling = true
	mgr.coolingSinceCount = 5

	if err := mgr.Reset(context.Background()); err != nil {
		t.Fatalf("Reset returned error: %v", err)
	}

	if mgr.msgCount != 0 {
		t.Errorf("expected msgCount=0 after Reset; got %d", mgr.msgCount)
	}
	if mgr.compressedAtCount != 0 {
		t.Errorf("expected compressedAtCount=0 after Reset; got %d", mgr.compressedAtCount)
	}
	if mgr.cooling {
		t.Error("expected cooling=false after Reset")
	}
	if mgr.coolingSinceCount != 0 {
		t.Errorf("expected coolingSinceCount=0 after Reset; got %d", mgr.coolingSinceCount)
	}
	if !mgr.lastCompressedAt.IsZero() {
		t.Errorf("expected lastCompressedAt to be zero after Reset; got %v", mgr.lastCompressedAt)
	}
	if mgr.lastCompressionGain != 0 {
		t.Errorf("expected lastCompressionGain=0 after Reset; got %v", mgr.lastCompressionGain)
	}
}

// TestReset_DeletesArchiveFile verifies that after Reset, the archive .db file
// no longer exists on disk.
func TestReset_DeletesArchiveFile(t *testing.T) {
	const key = "test-session"
	archiveDir := t.TempDir()
	store := newResetStore(key)
	mgr := newResetManager(store, key, WithArchiveDir(archiveDir))

	// Trigger lazy archive creation by appending a message.
	mgr.archiveAppend(providers.Message{Role: "user", Content: "hello"})

	// Confirm the file exists.
	sanitized := sanitizeSessionKey(key)
	archivePath := filepath.Join(archiveDir, sanitized+".archive.db")
	if _, err := os.Stat(archivePath); os.IsNotExist(err) {
		t.Fatal("precondition: archive file should exist before Reset")
	}

	if err := mgr.Reset(context.Background()); err != nil {
		t.Fatalf("Reset returned error: %v", err)
	}

	if _, err := os.Stat(archivePath); !os.IsNotExist(err) {
		t.Errorf("expected archive file to be deleted after Reset; Stat error: %v", err)
	}

	// archive reference must be nil.
	mgr.archiveMu.Lock()
	archiveRef := mgr.archive
	mgr.archiveMu.Unlock()
	if archiveRef != nil {
		t.Error("expected archive reference to be nil after Reset")
	}
}

// TestReset_ArchiveStartsFreshAfterNewMessages verifies that after Reset, a
// subsequent archive write creates a fresh archive starting from seq 1.
func TestReset_ArchiveStartsFreshAfterNewMessages(t *testing.T) {
	const key = "test-session"
	archiveDir := t.TempDir()
	store := newResetStore(key)
	mgr := newResetManager(store, key, WithArchiveDir(archiveDir))

	// Write a few messages to the archive before reset.
	for range 3 {
		content := strings.Repeat("x", 20)
		mgr.archiveAppend(providers.Message{Role: "user", Content: content})
	}

	// Reset wipes the archive.
	if err := mgr.Reset(context.Background()); err != nil {
		t.Fatalf("Reset returned error: %v", err)
	}

	// Write a new message. After Reset, archiveSeq is 0 so the first write uses seq 1.
	mgr.archiveAppend(providers.Message{Role: "user", Content: "fresh start"})

	// Verify the archive is available and bounds start at seq 1.
	archive := mgr.getOrOpenArchive()
	if archive == nil {
		t.Fatal("expected archive to be re-opened after Reset + new write")
	}

	minSeq, maxSeq, err := archive.Bounds()
	if err != nil {
		t.Fatalf("Bounds() returned error: %v", err)
	}
	if minSeq != 1 {
		t.Errorf("expected minSeq=1 after fresh start; got %d", minSeq)
	}
	if maxSeq != 1 {
		t.Errorf("expected maxSeq=1 after single write; got %d", maxSeq)
	}
}

// TestReset_ClearsPendingTurn verifies that ClearPendingTurn is called during Reset.
// We test this via the mockStore's side-effect tracking.
func TestReset_ClearsPendingTurn(t *testing.T) {
	const key = "test-session"
	store := newResetStore(key)

	// Track whether ClearPendingTurn was called by embedding a custom store.
	called := false
	trackingStore := &clearPendingTrackingStore{
		mockStore: store,
		onClear:   func() { called = true },
	}

	mgr := newResetManagerWithStore(trackingStore, key)

	if err := mgr.Reset(context.Background()); err != nil {
		t.Fatalf("Reset returned error: %v", err)
	}

	if !called {
		t.Error("expected ClearPendingTurn to be called during Reset")
	}
}

// clearPendingTrackingStore wraps mockStore and calls onClear when
// ClearPendingTurn is invoked.
type clearPendingTrackingStore struct {
	*mockStore
	onClear func()
}

func (s *clearPendingTrackingStore) ClearPendingTurn(_ string) error {
	s.onClear()
	return nil
}

// newResetManagerWithStore constructs a Manager using any SessionStore.
func newResetManagerWithStore(store interface {
	AddMessage(sessionKey, role, content string)
	AddFullMessage(sessionKey string, msg providers.Message)
	GetHistory(key string) []providers.Message
	GetHistoryWithSeqs(key string) []memory.StoredMessage
	GetSummary(key string) string
	SetSummary(key, summary string)
	SetHistory(key string, history []providers.Message)
	TruncateHistory(key string, keepLast int)
	SetPendingTurn(sessionKey string) error
	ClearPendingTurn(sessionKey string) error
	GetArchiveBounds(sessionKey string) (int, int)
	ListPendingSessions() ([]string, error)
	Save(key string) error
	Close() error
}, sessionKey string, opts ...Option) *Manager {
	baseOpts := []Option{
		WithContextWindow(10000),
		WithNormalPercent(90),
		WithSafetyPercent(95),
		WithMessageThreshold(100),
	}
	baseOpts = append(baseOpts, opts...)
	cm := New(sessionKey, store, nil, nil, baseOpts...)
	return cm.(*Manager)
}
