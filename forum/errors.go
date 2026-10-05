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
	// ErrParticipantGone: a temporary participant recorded in
	// participants.json no longer exists; the run fails rather than
	// recreating it without its history (§8).
	ErrParticipantGone = errors.New("temporary participant no longer exists")
	// ErrCorrupt: the forum directory fails verification (missing files,
	// digest mismatch, unreadable commit).
	ErrCorrupt = errors.New("forum directory is corrupt")

	errNotImplemented = errors.New("forum: not implemented")
)
