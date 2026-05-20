// ClawEh
// License: MIT

package llmcontext

import "github.com/PivotLLM/ClawEh/pkg/memory"

// CompactionStateStore is an optional interface that session store backends
// may implement to persist compression state across process restarts.
// Manager checks for this interface via type assertion; backends that do not
// implement it (e.g. in-memory SessionManager) use zero-state initialization.
//
// CompactionState is defined in pkg/memory so that implementing backends in
// pkg/session can satisfy this interface without importing pkg/llmcontext
// (which would create a circular import). Go's structural typing means
// JSONLBackend satisfies CompactionStateStore without an explicit declaration.
type CompactionStateStore interface {
	GetCompactionState(sessionKey string) (memory.CompactionState, error)
	SetCompactionState(sessionKey string, state memory.CompactionState) error
}
