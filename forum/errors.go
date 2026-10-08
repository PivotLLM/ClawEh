// ClawEh
// License: MIT
//
// Copyright (c) 2026 Tenebris Technologies Inc.

package forum

import "errors"

// Sentinel errors callers test with errors.Is. Everything else is wrapped
// context (fmt.Errorf with %w) around these or around I/O errors.
var (
	// ErrNotFound: no forum with that ID exists in the caller's base scope.
	ErrNotFound = errors.New("forum not found")
	// ErrLocked: another process holds the forum's run lock.
	ErrLocked = errors.New("forum is locked by another process")
	// ErrInvalidState: the operation is not allowed in the forum's current
	// state (pause a terminal forum, delete a running one, resume a forum
	// that is not paused or interrupted).
	ErrInvalidState = errors.New("operation not allowed in this state")
	// ErrSchemasUnavailable: the configuration names a JSON Schema but the
	// host provided no SchemaValidator.
	ErrSchemasUnavailable = errors.New("JSON Schema validation is not available")
	// ErrCorrupt: the forum directory fails verification (missing files,
	// digest mismatch, unreadable commit).
	ErrCorrupt = errors.New("forum directory is corrupt")
	// ErrShuttingDown: the host is shutting down. Messenger.Ask returns an
	// error wrapping it when it cannot deliver or finish a turn for that
	// reason; the attempt stays uncertain and the forum resumes at the next
	// start instead of failing.
	ErrShuttingDown = errors.New("the host is shutting down")
	// ErrForumTurn: a forum tool was called by a temporary agent a forum
	// created. ToolHost.Scope returns an error wrapping it and the tool is
	// refused, so a forum can never launch or control forums.
	ErrForumTurn = errors.New("forum tools are not available inside a forum turn")
	// ErrForumDepth: a forum tool was called from a turn at the maximum
	// sub-agent depth (where every forum turn runs, but also any other
	// turn that deep). ToolHost.Scope returns an error wrapping it and the
	// tool is refused.
	ErrForumDepth = errors.New("forum tools are not available at the maximum sub-agent depth")
	// ErrDeletePending: Agents.Delete found the agent in a turn and will
	// delete it when that turn ends. The forum keeps it in its cleanup
	// marker and retries, so a restart before the turn ends still cleans
	// it up, but it is not a failure.
	ErrDeletePending = errors.New("the agent is deleted when its turn ends")
)
