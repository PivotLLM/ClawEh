package callback

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// writeFakeStore writes a Store directly to disk for test setup.
func writeFakeStore(t *testing.T, path string, s Store) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	data, err := json.Marshal(s)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
}

func TestNewManager_Disabled_ReturnsNil(t *testing.T) {
	dir := t.TempDir()
	storePath := filepath.Join(dir, "state", "callback.json")

	mgr, err := NewManager("alice", storePath, 0, 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if mgr != nil {
		mgr.Stop()
		t.Fatal("expected nil manager when windowMinutes==0")
	}
}

func TestNewManager_Disabled_CleansUpFile(t *testing.T) {
	dir := t.TempDir()
	storePath := filepath.Join(dir, "state", "callback.json")

	// Pre-create a store file.
	writeFakeStore(t, storePath, Store{
		Tokens:         []Token{{Value: "oldtoken", ExpiresAt: time.Now().Add(10 * time.Minute).Unix()}},
		NextRotationAt: time.Now().Add(5 * time.Minute).Unix(),
	})

	mgr, err := NewManager("alice", storePath, 0, 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if mgr != nil {
		mgr.Stop()
		t.Fatal("expected nil manager when windowMinutes==0")
	}

	if _, err := os.Stat(storePath); !os.IsNotExist(err) {
		t.Error("expected store file to be removed when disabled")
	}
}

func TestNewManager_CreatesInitialToken(t *testing.T) {
	dir := t.TempDir()
	storePath := filepath.Join(dir, "state", "callback.json")

	mgr, err := NewManager("alice", storePath, 5, 3)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if mgr == nil {
		t.Fatal("expected non-nil manager")
	}
	defer mgr.Stop()

	tok := mgr.CurrentToken()
	if tok == "" {
		t.Fatal("expected a non-empty initial token")
	}
	if len(tok) != 64 {
		t.Errorf("expected 64-char hex token, got len %d", len(tok))
	}

	// Store file should exist.
	if _, err := os.Stat(storePath); err != nil {
		t.Errorf("store file missing: %v", err)
	}
}

func TestValidate_ValidToken(t *testing.T) {
	dir := t.TempDir()
	storePath := filepath.Join(dir, "state", "callback.json")

	mgr, err := NewManager("alice", storePath, 5, 3)
	if err != nil || mgr == nil {
		t.Fatalf("setup failed: err=%v mgr=%v", err, mgr)
	}
	defer mgr.Stop()

	tok := mgr.CurrentToken()
	if !mgr.Validate(tok) {
		t.Error("expected Validate to return true for current token")
	}
}

func TestValidate_InvalidToken(t *testing.T) {
	dir := t.TempDir()
	storePath := filepath.Join(dir, "state", "callback.json")

	mgr, err := NewManager("alice", storePath, 5, 3)
	if err != nil || mgr == nil {
		t.Fatalf("setup failed: err=%v mgr=%v", err, mgr)
	}
	defer mgr.Stop()

	if mgr.Validate("notarealtoken") {
		t.Error("expected Validate to return false for unknown token")
	}
}

func TestValidate_ExpiredToken(t *testing.T) {
	dir := t.TempDir()
	storePath := filepath.Join(dir, "state", "callback.json")

	// Write a store with an already-expired token.
	writeFakeStore(t, storePath, Store{
		Tokens: []Token{
			{Value: "expiredtoken", ExpiresAt: time.Now().Add(-1 * time.Minute).Unix()},
		},
		NextRotationAt: time.Now().Add(-1 * time.Minute).Unix(),
	})

	mgr, err := NewManager("alice", storePath, 5, 3)
	if err != nil || mgr == nil {
		t.Fatalf("setup failed: err=%v mgr=%v", err, mgr)
	}
	defer mgr.Stop()

	// The expired token should not be valid.
	if mgr.Validate("expiredtoken") {
		t.Error("expected expired token to be invalid")
	}
}

func TestValidate_MultipleWindowsOfTokens(t *testing.T) {
	dir := t.TempDir()
	storePath := filepath.Join(dir, "state", "callback.json")

	// Seed three valid tokens simulating windowCount=3.
	futureExpiry := time.Now().Add(15 * time.Minute).Unix()
	writeFakeStore(t, storePath, Store{
		Tokens: []Token{
			{Value: "token1", ExpiresAt: futureExpiry},
			{Value: "token2", ExpiresAt: futureExpiry},
			{Value: "token3", ExpiresAt: futureExpiry},
		},
		NextRotationAt: time.Now().Add(5 * time.Minute).Unix(),
	})

	mgr, err := NewManager("alice", storePath, 5, 3)
	if err != nil || mgr == nil {
		t.Fatalf("setup failed: err=%v mgr=%v", err, mgr)
	}
	defer mgr.Stop()

	// All pre-seeded tokens must still validate (they are still unexpired and
	// the manager loaded them from disk; rotation may have added one more).
	for _, tok := range []string{"token1", "token2", "token3"} {
		if !mgr.Validate(tok) {
			t.Errorf("expected token %q to be valid", tok)
		}
	}
}

func TestRestart_PreservesExistingTokens(t *testing.T) {
	dir := t.TempDir()
	storePath := filepath.Join(dir, "state", "callback.json")

	// First run — create manager, capture token.
	mgr1, err := NewManager("alice", storePath, 60, 3)
	if err != nil || mgr1 == nil {
		t.Fatalf("first setup failed: %v", err)
	}
	tok := mgr1.CurrentToken()
	mgr1.Stop()

	// Second run — reload from disk.
	mgr2, err := NewManager("alice", storePath, 60, 3)
	if err != nil || mgr2 == nil {
		t.Fatalf("second setup failed: %v", err)
	}
	defer mgr2.Stop()

	// The token from the first run must still be valid.
	if !mgr2.Validate(tok) {
		t.Error("expected token from first run to remain valid after restart")
	}
}

func TestPruning_RemovesExpiredTokens(t *testing.T) {
	dir := t.TempDir()
	storePath := filepath.Join(dir, "state", "callback.json")

	// Seed one expired and one valid token; rotation is not yet due.
	writeFakeStore(t, storePath, Store{
		Tokens: []Token{
			{Value: "expired", ExpiresAt: time.Now().Add(-1 * time.Minute).Unix()},
			{Value: "valid", ExpiresAt: time.Now().Add(10 * time.Minute).Unix()},
		},
		NextRotationAt: time.Now().Add(5 * time.Minute).Unix(),
	})

	mgr, err := NewManager("alice", storePath, 5, 3)
	if err != nil || mgr == nil {
		t.Fatalf("setup failed: %v", err)
	}
	defer mgr.Stop()

	if mgr.Validate("expired") {
		t.Error("expired token should have been pruned")
	}
	if !mgr.Validate("valid") {
		t.Error("valid token should still be accepted")
	}
}
