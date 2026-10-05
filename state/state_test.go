package state

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestAtomicSave(t *testing.T) {
	tmpDir := t.TempDir()
	sm := NewManager(tmpDir)

	want := PendingTurn{Channel: "telegram", ChatID: "123"}
	if err := sm.SetPendingTurn("agent:alice:main", want); err != nil {
		t.Fatalf("SetPendingTurn failed: %v", err)
	}
	if got, ok := sm.GetPendingTurn("agent:alice:main"); !ok || got != want {
		t.Errorf("GetPendingTurn = %+v, %v; want %+v", got, ok, want)
	}
	if sm.GetTimestamp().IsZero() {
		t.Error("Expected timestamp to be updated")
	}

	stateFile := filepath.Join(tmpDir, "state", "state.json")
	if _, err := os.Stat(stateFile); os.IsNotExist(err) {
		t.Error("Expected state file to exist")
	}

	// A new manager on the same workspace reads the saved state back.
	if got, ok := NewManager(tmpDir).GetPendingTurn("agent:alice:main"); !ok || got != want {
		t.Errorf("reloaded GetPendingTurn = %+v, %v; want %+v", got, ok, want)
	}
}

func TestAtomicity_NoCorruptionOnInterrupt(t *testing.T) {
	tmpDir := t.TempDir()
	sm := NewManager(tmpDir)

	if err := sm.SetPendingTurn("k", PendingTurn{Channel: "initial"}); err != nil {
		t.Fatalf("SetPendingTurn failed: %v", err)
	}

	// Simulate a crash scenario by manually creating a corrupted temp file
	tempFile := filepath.Join(tmpDir, "state", "state.json.tmp")
	if err := os.WriteFile(tempFile, []byte("corrupted data"), 0o644); err != nil {
		t.Fatalf("Failed to create temp file: %v", err)
	}

	// The original state is still intact.
	if got, _ := NewManager(tmpDir).GetPendingTurn("k"); got.Channel != "initial" {
		t.Errorf("Expected channel 'initial' after corrupted temp file, got %q", got.Channel)
	}

	if rmErr := os.Remove(tempFile); rmErr != nil {
		t.Fatalf("Failed to remove temp file: %v", rmErr)
	}

	if err := sm.SetPendingTurn("k", PendingTurn{Channel: "new"}); err != nil {
		t.Fatalf("SetPendingTurn failed: %v", err)
	}
	if got, _ := sm.GetPendingTurn("k"); got.Channel != "new" {
		t.Errorf("Expected channel 'new', got %q", got.Channel)
	}
}

func TestConcurrentAccess(t *testing.T) {
	tmpDir := t.TempDir()
	sm := NewManager(tmpDir)

	done := make(chan bool, 10)
	for i := range 10 {
		go func(idx int) {
			key := fmt.Sprintf("session-%d", idx)
			if setErr := sm.SetPendingTurn(key, PendingTurn{Channel: "c"}); setErr != nil {
				t.Errorf("SetPendingTurn(%s): %v", key, setErr)
			}
			done <- true
		}(i)
	}
	for range 10 {
		<-done
	}

	for i := range 10 {
		if _, ok := sm.GetPendingTurn(fmt.Sprintf("session-%d", i)); !ok {
			t.Errorf("session-%d missing after concurrent writes", i)
		}
	}

	// The state file is valid JSON.
	data, err := os.ReadFile(filepath.Join(tmpDir, "state", "state.json"))
	if err != nil {
		t.Fatalf("Failed to read state file: %v", err)
	}
	var state State
	if err := json.Unmarshal(data, &state); err != nil {
		t.Errorf("State file contains invalid JSON: %v", err)
	}
}

func TestNewManager_EmptyWorkspace(t *testing.T) {
	sm := NewManager(t.TempDir())
	if _, ok := sm.GetPendingTurn("agent:alice:main"); ok {
		t.Error("Expected no pending turn in a new state")
	}
	if !sm.GetTimestamp().IsZero() {
		t.Error("Expected zero timestamp for new state")
	}
}

func TestNewManager_MkdirFailureDoesNotCrash(t *testing.T) {
	if os.Getenv("BE_CRASHER") == "1" {
		tmpDir := os.Getenv("CRASH_DIR")

		statePath := filepath.Join(tmpDir, "state")
		if err := os.WriteFile(statePath, []byte("I'm a file, not a folder"), 0o644); err != nil {
			fmt.Printf("setup failed: %v", err)
			os.Exit(0)
		}

		NewManager(tmpDir)
		os.Exit(0)
	}

	tmpDir := t.TempDir()

	cmd := exec.Command(os.Args[0], "-test.run=TestNewManager_MkdirFailureDoesNotCrash")
	cmd.Env = append(os.Environ(), "BE_CRASHER=1", "CRASH_DIR="+tmpDir)

	err := cmd.Run()
	if err != nil {
		t.Fatalf("NewManager should not crash when state dir creation fails, got: %v", err)
	}
}
