package session

import (
	"context"
	"log"

	"github.com/PivotLLM/ClawEh/pkg/memory"
	"github.com/PivotLLM/ClawEh/pkg/providers"
)

// JSONLBackend adapts a memory.Store into the SessionStore interface.
// Write errors are logged rather than returned, matching the fire-and-forget
// contract of SessionManager that the agent loop relies on.
type JSONLBackend struct {
	store memory.Store
}

// NewJSONLBackend wraps a memory.Store for use as a SessionStore.
func NewJSONLBackend(store memory.Store) *JSONLBackend {
	return &JSONLBackend{store: store}
}

func (b *JSONLBackend) AddMessage(sessionKey, role, content string) {
	if err := b.store.AddMessage(context.Background(), sessionKey, role, content); err != nil {
		log.Printf("session: add message: %v", err)
	}
}

func (b *JSONLBackend) AddFullMessage(sessionKey string, msg providers.Message) int64 {
	seq, err := b.store.AddFullMessage(context.Background(), sessionKey, msg)
	if err != nil {
		log.Printf("session: add full message: %v", err)
		return 0
	}
	return seq
}

func (b *JSONLBackend) GetHistory(key string) []providers.Message {
	msgs, err := b.store.GetHistory(context.Background(), key)
	if err != nil {
		log.Printf("session: get history: %v", err)
		return []providers.Message{}
	}
	return msgs
}

func (b *JSONLBackend) GetHistoryWithSeqs(key string) []memory.StoredMessage {
	stored, err := b.store.GetHistoryWithSeqs(context.Background(), key)
	if err != nil {
		log.Printf("session: get history with seqs: %v", err)
		return []memory.StoredMessage{}
	}
	return stored
}

func (b *JSONLBackend) GetSummary(key string) string {
	summary, err := b.store.GetSummary(context.Background(), key)
	if err != nil {
		log.Printf("session: get summary: %v", err)
		return ""
	}
	return summary
}

func (b *JSONLBackend) SetSummary(key, summary string) {
	if err := b.store.SetSummary(context.Background(), key, summary); err != nil {
		log.Printf("session: set summary: %v", err)
	}
}

func (b *JSONLBackend) SetHistory(key string, history []providers.Message) {
	if err := b.store.SetHistory(context.Background(), key, history); err != nil {
		log.Printf("session: set history: %v", err)
	}
}

// SetHistoryWithSeqs replaces the session history while preserving stable
// sequence numbers. Context compaction uses this so summary citations remain
// linked to archive message IDs.
func (b *JSONLBackend) SetHistoryWithSeqs(key string, history []memory.StoredMessage) {
	if err := b.store.SetHistoryWithSeqs(context.Background(), key, history); err != nil {
		log.Printf("session: set history with seqs: %v", err)
	}
}

// AppendSummaryCheckpoint appends one summary checkpoint when the underlying
// store supports checkpoint history.
func (b *JSONLBackend) AppendSummaryCheckpoint(sessionKey string, checkpoint memory.SummaryCheckpoint) error {
	type checkpointAppender interface {
		AppendSummaryCheckpoint(context.Context, string, memory.SummaryCheckpoint) error
	}
	if a, ok := b.store.(checkpointAppender); ok {
		return a.AppendSummaryCheckpoint(context.Background(), sessionKey, checkpoint)
	}
	return nil
}

func (b *JSONLBackend) TruncateHistory(key string, keepLast int) {
	if err := b.store.TruncateHistory(context.Background(), key, keepLast); err != nil {
		log.Printf("session: truncate history: %v", err)
	}
}

func (b *JSONLBackend) SetPendingTurn(sessionKey string) error {
	return b.store.SetPendingTurn(context.Background(), sessionKey)
}

func (b *JSONLBackend) ClearPendingTurn(sessionKey string) error {
	return b.store.ClearPendingTurn(context.Background(), sessionKey)
}

func (b *JSONLBackend) ListPendingSessions() ([]string, error) {
	return b.store.ListPendingSessions(context.Background())
}

func (b *JSONLBackend) GetArchiveBounds(sessionKey string) (minSeq, maxSeq int64) {
	min, max, err := b.store.GetArchiveBounds(context.Background(), sessionKey)
	if err != nil {
		log.Printf("session: get archive bounds: %v", err)
		return 0, 0
	}
	return min, max
}

// Save persists session state. Since the JSONL store fsyncs every write
// immediately, the data is already durable. Save runs compaction to reclaim
// space from logically truncated messages (no-op when there are none).
func (b *JSONLBackend) Save(key string) error {
	return b.store.Compact(context.Background(), key)
}

// GetCompactionState retrieves the durable compression state for sessionKey.
// Implements the llmcontext.CompactionStateStore interface via structural typing.
// Returns a zero CompactionState if the underlying store does not support it.
func (b *JSONLBackend) GetCompactionState(sessionKey string) (memory.CompactionState, error) {
	type compactionGetter interface {
		GetCompactionState(sessionKey string) (memory.CompactionState, error)
	}
	if g, ok := b.store.(compactionGetter); ok {
		return g.GetCompactionState(sessionKey)
	}
	return memory.CompactionState{}, nil
}

// SetCompactionState persists the compression state for sessionKey.
// Implements the llmcontext.CompactionStateStore interface via structural typing.
// No-ops if the underlying store does not support persistent compaction state.
func (b *JSONLBackend) SetCompactionState(sessionKey string, state memory.CompactionState) error {
	type compactionSetter interface {
		SetCompactionState(sessionKey string, state memory.CompactionState) error
	}
	if s, ok := b.store.(compactionSetter); ok {
		return s.SetCompactionState(sessionKey, state)
	}
	return nil
}

// ForgetSession drops in-memory per-session state held by the underlying store
// (e.g. the noise-dedup cache). Durable data on disk is untouched. No-ops if
// the underlying store does not support it. Called when a session's context
// manager is evicted so per-session caches do not grow unbounded.
func (b *JSONLBackend) ForgetSession(key string) {
	type sessionForgetter interface {
		ForgetSession(key string)
	}
	if f, ok := b.store.(sessionForgetter); ok {
		f.ForgetSession(key)
	}
}

// Close releases resources held by the underlying store.
func (b *JSONLBackend) Close() error {
	return b.store.Close()
}
