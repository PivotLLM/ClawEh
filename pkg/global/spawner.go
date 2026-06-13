// ClawEh
// License: MIT

package global

import "context"

// SpawnMode selects how a sub-agent worker is launched.
type SpawnMode int

const (
	// SpawnDetached fires the worker and returns immediately; its result is
	// discarded by the spawner (fire-and-forget, "nothing").
	SpawnDetached SpawnMode = iota
	// SpawnCallback fires the worker and returns immediately; when it finishes,
	// its result is delivered to SpawnRequest.OnResult. The host typically wires
	// OnResult so the result is injected back onto the originating channel — the
	// same shape as an async tool callback.
	SpawnCallback
	// SpawnAndWait runs the worker to completion and returns its result
	// synchronously from Spawn.
	SpawnAndWait
)

// String renders a SpawnMode for logs/diagnostics.
func (m SpawnMode) String() string {
	switch m {
	case SpawnDetached:
		return "detached"
	case SpawnCallback:
		return "callback"
	case SpawnAndWait:
		return "wait"
	default:
		return "unknown"
	}
}

// SpawnRequest describes one worker launch.
type SpawnRequest struct {
	Mode          SpawnMode
	Task          string // the worker's instructions (required)
	Label         string // optional short display label
	TargetAgentID string // "" ⇒ self-spawn (caller's own agent)
	Channel       string // originating channel (for attribution / callback routing)
	ChatID        string // originating chat id
	// OnResult receives the worker's result for SpawnCallback. Ignored for the
	// other modes. The host supplies it; a nil OnResult in callback mode degrades
	// to detached (the worker still runs, the result is dropped).
	OnResult func(*Result)
}

// Spawner launches sub-agent workers in one of three modes. The host injects a
// concrete implementation via Deps.Spawn so both the internal spawn tool and any
// external/MCP tool can launch workers through the same robust path.
//
// For SpawnDetached and SpawnCallback, Spawn returns promptly with an
// acknowledgement Result (the worker runs in the background). For SpawnAndWait it
// blocks until the worker finishes and returns the worker's own Result.
type Spawner interface {
	Spawn(ctx context.Context, req SpawnRequest) (*Result, error)
}
