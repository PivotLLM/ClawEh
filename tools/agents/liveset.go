// ClawEh
// License: MIT

package agents

import "sync"

// LiveSet tracks the uuids of callback tasks with a live worker goroutine in
// THIS process. It is shared across every SubagentManager built by one AgentLoop
// (including across config reloads) so the supervisor never relaunches a task
// that is still running on an older manager. After a process restart the set is
// empty, so leftover .run markers are correctly seen as interrupted.
type LiveSet struct {
	mu sync.Mutex
	m  map[string]struct{}
}

// NewLiveSet returns an empty LiveSet.
func NewLiveSet() *LiveSet {
	return &LiveSet{m: make(map[string]struct{})}
}

// Add marks a uuid as running.
func (l *LiveSet) Add(uuid string) {
	if l == nil {
		return
	}
	l.mu.Lock()
	l.m[uuid] = struct{}{}
	l.mu.Unlock()
}

// Remove clears a uuid's running mark.
func (l *LiveSet) Remove(uuid string) {
	if l == nil {
		return
	}
	l.mu.Lock()
	delete(l.m, uuid)
	l.mu.Unlock()
}

// Has reports whether a uuid is currently running.
func (l *LiveSet) Has(uuid string) bool {
	if l == nil {
		return false
	}
	l.mu.Lock()
	_, ok := l.m[uuid]
	l.mu.Unlock()
	return ok
}
