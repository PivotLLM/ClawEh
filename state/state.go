package state

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/PivotLLM/ClawEh/fileutil"
	"github.com/PivotLLM/ClawEh/logger"
)

// State represents the persistent state for a workspace.
// It includes information about the last active channel/chat.
type State struct {
	// LastChannel is the last channel used for communication
	LastChannel string `json:"last_channel,omitempty"`

	// LastChatID is the last chat ID used for communication
	LastChatID string `json:"last_chat_id,omitempty"`

	// Timestamp is the last time this state was updated
	Timestamp time.Time `json:"timestamp"`

	// PendingTurns records, per session key, where the turn now in flight came
	// from and how often recovery has replayed it. An entry exists only while
	// the session store's pending-turn flag is set.
	PendingTurns map[string]PendingTurn `json:"pending_turns,omitempty"`
}

// PendingTurn is the source of an in-flight turn, kept so a restart can replay
// the interrupted message and deliver the reply where the user is waiting.
type PendingTurn struct {
	Channel  string `json:"channel"`
	ChatID   string `json:"chat_id"`
	Attempts int    `json:"attempts,omitempty"` // recovery replays so far
}

// Manager manages persistent state with atomic saves.
type Manager struct {
	workspace string
	state     *State
	mu        sync.RWMutex
	stateFile string
}

// NewManager creates a new state manager for the given workspace.
func NewManager(workspace string) *Manager {
	stateDir := filepath.Join(workspace, "state")
	stateFile := filepath.Join(stateDir, "state.json")
	oldStateFile := filepath.Join(workspace, "state.json")

	// Create state directory if it doesn't exist
	if err := os.MkdirAll(stateDir, 0o700); err != nil { //nolint:gosec // workspace path from agent config
		logger.WarnCF("state", "failed to create state directory",
			map[string]any{"path": stateDir, "error": err.Error()})
	}

	sm := &Manager{
		workspace: workspace,
		stateFile: stateFile,
		state:     &State{},
	}

	// Try to load from new location first
	if _, err := os.Stat(stateFile); os.IsNotExist(err) { //nolint:gosec // workspace path from agent config
		// New file doesn't exist, try migrating from old location
		if data, err := os.ReadFile(oldStateFile); err == nil { //nolint:gosec // legacy state file under the agent workspace
			if err := json.Unmarshal(data, sm.state); err == nil {
				// Migrate to new location
				if err := sm.saveAtomic(); err != nil {
					logger.WarnCF("state", "failed to save state",
						map[string]any{"error": err.Error()})
				}
				logger.InfoCF("state", "migrated state",
					map[string]any{"from": oldStateFile, "to": stateFile})
			}
		}
	} else {
		// Load from new location
		if err := sm.load(); err != nil {
			logger.WarnCF("state", "failed to load state",
				map[string]any{"error": err.Error()})
		}
	}

	return sm
}

// SetLastChannel atomically updates the last channel and saves the state.
// This method uses a temp file + rename pattern for atomic writes,
// ensuring that the state file is never corrupted even if the process crashes.
func (sm *Manager) SetLastChannel(channel string) error {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	// Update state
	sm.state.LastChannel = channel
	sm.state.Timestamp = time.Now()

	// Atomic save using temp file + rename
	if err := sm.saveAtomic(); err != nil {
		return fmt.Errorf("failed to save state atomically: %w", err)
	}

	return nil
}

// SetLastChatID atomically updates the last chat ID and saves the state.
func (sm *Manager) SetLastChatID(chatID string) error {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	// Update state
	sm.state.LastChatID = chatID
	sm.state.Timestamp = time.Now()

	// Atomic save using temp file + rename
	if err := sm.saveAtomic(); err != nil {
		return fmt.Errorf("failed to save state atomically: %w", err)
	}

	return nil
}

// GetLastChannel returns the last channel from the state.
func (sm *Manager) GetLastChannel() string {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	return sm.state.LastChannel
}

// GetLastChatID returns the last chat ID from the state.
func (sm *Manager) GetLastChatID() string {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	return sm.state.LastChatID
}

// SetPendingTurn records the source of the turn in flight for sessionKey and
// saves the state. It keeps the existing Attempts count so a recovery replay
// does not reset its own counter; pass the returned record back to
// SetPendingTurn with Attempts changed to update the count.
func (sm *Manager) SetPendingTurn(sessionKey string, pt PendingTurn) error {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	if sm.state.PendingTurns == nil {
		sm.state.PendingTurns = make(map[string]PendingTurn)
	}
	if pt.Attempts == 0 {
		pt.Attempts = sm.state.PendingTurns[sessionKey].Attempts
	}
	sm.state.PendingTurns[sessionKey] = pt
	sm.state.Timestamp = time.Now()

	if err := sm.saveAtomic(); err != nil {
		return fmt.Errorf("failed to save state atomically: %w", err)
	}
	return nil
}

// GetPendingTurn returns the recorded source of sessionKey's in-flight turn.
func (sm *Manager) GetPendingTurn(sessionKey string) (PendingTurn, bool) {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	pt, ok := sm.state.PendingTurns[sessionKey]
	return pt, ok
}

// ClearPendingTurn forgets sessionKey's in-flight turn and saves the state.
// A no-op when nothing is recorded.
func (sm *Manager) ClearPendingTurn(sessionKey string) error {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	if _, ok := sm.state.PendingTurns[sessionKey]; !ok {
		return nil
	}
	delete(sm.state.PendingTurns, sessionKey)
	sm.state.Timestamp = time.Now()

	if err := sm.saveAtomic(); err != nil {
		return fmt.Errorf("failed to save state atomically: %w", err)
	}
	return nil
}

// GetTimestamp returns the timestamp of the last state update.
func (sm *Manager) GetTimestamp() time.Time {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	return sm.state.Timestamp
}

// saveAtomic performs an atomic save using temp file + rename.
// This ensures that the state file is never corrupted:
// 1. Write to a temp file
// 2. Sync to disk (critical for SD cards/flash storage)
// 3. Rename temp file to target (atomic on POSIX systems)
// 4. If rename fails, cleanup the temp file
//
// Must be called with the lock held.
func (sm *Manager) saveAtomic() error {
	// Use unified atomic write utility with explicit sync for flash storage reliability.
	// Using 0o600 (owner read/write only) for secure default permissions.
	data, err := json.MarshalIndent(sm.state, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal state: %w", err)
	}

	return fileutil.WriteFileAtomic(sm.stateFile, data, 0o600)
}

// load loads the state from disk.
func (sm *Manager) load() error {
	data, err := os.ReadFile(sm.stateFile)
	if err != nil {
		// File doesn't exist yet, that's OK
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("failed to read state file: %w", err)
	}

	if err := json.Unmarshal(data, sm.state); err != nil {
		return fmt.Errorf("failed to unmarshal state: %w", err)
	}

	return nil
}
