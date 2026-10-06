package agent

import (
	"github.com/PivotLLM/ctxengine/memory"
	"github.com/PivotLLM/ctxengine/session"
	"github.com/PivotLLM/spawnllm"
)

// privateSessionStore is the engine's SQLite session store with one
// addition: before any call that names a session, the session's archive
// database is created empty and 0600 when it does not exist yet (SQLite
// would create it, and its -wal/-shm, 0644). Every reader and writer of an
// agent's Sessions goes through it, so no path creates an archive readable by
// others. Methods that name no session are promoted unchanged, as are the
// optional interfaces the engine looks for (compaction state, commits).
type privateSessionStore struct {
	*session.SQLiteStore
	dir string
}

func (s *privateSessionStore) ensure(key string) { ensureArchiveIn(s.dir, key) }

func (s *privateSessionStore) AddMessage(key, role, content string) error {
	s.ensure(key)
	return s.SQLiteStore.AddMessage(key, role, content)
}

func (s *privateSessionStore) AddFullMessage(key string, msg spawnllm.Message) (int64, error) {
	s.ensure(key)
	return s.SQLiteStore.AddFullMessage(key, msg)
}

func (s *privateSessionStore) GetHistory(key string) []spawnllm.Message {
	s.ensure(key)
	return s.SQLiteStore.GetHistory(key)
}

func (s *privateSessionStore) GetHistoryWithSeqs(key string) []memory.StoredMessage {
	s.ensure(key)
	return s.SQLiteStore.GetHistoryWithSeqs(key)
}

func (s *privateSessionStore) GetSummary(key string) string {
	s.ensure(key)
	return s.SQLiteStore.GetSummary(key)
}

func (s *privateSessionStore) SetSummary(key, summary string) error {
	s.ensure(key)
	return s.SQLiteStore.SetSummary(key, summary)
}

func (s *privateSessionStore) SetHistory(key string, history []spawnllm.Message) error {
	s.ensure(key)
	return s.SQLiteStore.SetHistory(key, history)
}

func (s *privateSessionStore) SetHistoryWithSeqs(key string, history []memory.StoredMessage) error {
	s.ensure(key)
	return s.SQLiteStore.SetHistoryWithSeqs(key, history)
}

func (s *privateSessionStore) CommitCompaction(key string, c session.CompactionCommit) error {
	s.ensure(key)
	return s.SQLiteStore.CommitCompaction(key, c)
}

func (s *privateSessionStore) TruncateHistory(key string, keepLast int) error {
	s.ensure(key)
	return s.SQLiteStore.TruncateHistory(key, keepLast)
}

func (s *privateSessionStore) SetPendingTurn(key string) error {
	s.ensure(key)
	return s.SQLiteStore.SetPendingTurn(key)
}

func (s *privateSessionStore) ClearPendingTurn(key string) error {
	s.ensure(key)
	return s.SQLiteStore.ClearPendingTurn(key)
}

func (s *privateSessionStore) GetArchiveBounds(key string) (minSeq, maxSeq int64) {
	s.ensure(key)
	return s.SQLiteStore.GetArchiveBounds(key)
}

func (s *privateSessionStore) GetCompactionState(key string) (memory.CompactionState, error) {
	s.ensure(key)
	return s.SQLiteStore.GetCompactionState(key)
}

func (s *privateSessionStore) SetCompactionState(key string, cs memory.CompactionState) error {
	s.ensure(key)
	return s.SQLiteStore.SetCompactionState(key, cs)
}
