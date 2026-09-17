// ClawEh
// License: MIT

package global

import (
	"context"
	"errors"
)

// SpawnMode selects how a sub-agent worker is launched.
type SpawnMode int

const (
	// SpawnCallback fires the worker and returns immediately with the task's
	// uuid. The full result is written to a file in the launcher's workspace and
	// the task is tracked (status-queryable, supervisor-recoverable). When it
	// finishes, a compact pointer is delivered to SpawnRequest.OnResult (the host
	// wires this to a short channel notification on the agent-loop path; MCP
	// clients poll agent_status instead).
	SpawnCallback SpawnMode = iota
	// SpawnAndWait runs the worker to completion and returns its result
	// synchronously from Spawn. Ephemeral: no task files are written, because if
	// the process dies mid-wait the blocking caller dies with it.
	SpawnAndWait
)

// String renders a SpawnMode for logs/diagnostics.
func (m SpawnMode) String() string {
	switch m {
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
	Task          string   // the worker's instructions (required)
	Name          string   // short label; required for callback mode (human handle)
	Label         string   // optional display label (used by wait mode)
	TargetAgentID string   // "" ⇒ self-spawn (caller's own agent)
	Model         string   // optional model (alias) for the sub-agent; must be one of the executing agent's configured models. "" ⇒ that agent's default.
	Media         []string // optional media:// refs attached to the worker's initial message (e.g. hand an image to a vision-capable agent)
	Channel       string   // originating channel (for attribution / callback routing)
	ChatID        string   // originating chat id
	// OnResult receives the compact completion pointer for SpawnCallback. Ignored
	// for SpawnAndWait. The host supplies it; a nil OnResult is fine — the worker
	// still runs and is tracked, the push is simply skipped (the result remains in
	// the file and is available via agent_status).
	OnResult func(*Result)
}

// Spawner launches sub-agent workers. The host injects a concrete implementation
// via Deps.Spawn so both the internal spawn tool and any external/MCP tool launch
// workers through the same robust, file-backed path.
//
// For SpawnCallback, Spawn returns promptly with an acknowledgement Result whose
// ForLLM carries the task uuid (the worker runs in the background). For
// SpawnAndWait it blocks until the worker finishes and returns the worker's own
// Result.
type Spawner interface {
	Spawn(ctx context.Context, req SpawnRequest) (*Result, error)
}

// TaskStatus is one task's state, returned by agent_status.
type TaskStatus struct {
	UUID       string `json:"uuid"`
	Name       string `json:"name"`
	Status     string `json:"status"` // unknown | running | done | error
	ResultFile string `json:"result_file,omitempty"`
	Error      string `json:"error,omitempty"`
	CreatedAt  string `json:"created_at,omitempty"`
	FinishedAt string `json:"finished_at,omitempty"`
	Restarts   int    `json:"restarts,omitempty"`
}

// TaskBrief is one row in agent_list.
type TaskBrief struct {
	UUID   string `json:"uuid"`
	Name   string `json:"name"`
	Status string `json:"status"`
}

// TaskInspector is an optional capability of a Spawner: query tracked callback
// tasks. The agent_status / agent_list tools recover it from Deps.Spawn via type
// assertion, so a Spawner that does not implement it simply disables those tools.
type TaskInspector interface {
	// TaskStatus returns the task with the given uuid. Status "unknown" (not an
	// error) when no such task exists.
	TaskStatus(uuid string) (*TaskStatus, error)
	// TaskList returns all tracked tasks for the owning agent.
	TaskList() ([]TaskBrief, error)
}

// TurnUsage is the resource accounting for one agent turn (all LLM iterations
// of a sub-agent run), as reported by the provider dispatch status.
type TurnUsage struct {
	Model               string  // model that produced the final response
	Provider            string  // protocol/provider that served it
	InputTokens         int     //
	OutputTokens        int     //
	CacheReadTokens     int     //
	CacheCreationTokens int     //
	CostUSD             float64 //
}

// Add accumulates one LLM call's accounting into the turn. model/provider are
// taken from the most recent call so the turn records what actually served it.
func (u *TurnUsage) Add(model, provider string, in, out, cacheRead, cacheCreate int, cost float64) {
	if u == nil {
		return
	}
	if model != "" {
		u.Model = model
	}
	if provider != "" {
		u.Provider = provider
	}
	u.InputTokens += in
	u.OutputTokens += out
	u.CacheReadTokens += cacheRead
	u.CacheCreationTokens += cacheCreate
	u.CostUSD += cost
}

// SyncResult is what a SyncRunner returns: the worker's raw content, how many
// LLM iterations it took, and the turn's resource accounting.
type SyncResult struct {
	Content    string
	Iterations int
	TurnUsage
}

// Errors a SyncRunner returns for failures that no retry can fix. Programmatic
// hosts (e.g. an embedded orchestrator) test them with errors.Is to fail the
// work immediately instead of retrying.
var (
	// ErrSpawnUnavailable: no sub-agent runner is configured for this agent.
	ErrSpawnUnavailable = errors.New("spawn is not available")
	// ErrSpawnDepthExceeded: the caller is already at the sub-agent depth bound.
	ErrSpawnDepthExceeded = errors.New("maximum sub-agent depth reached")
	// ErrModelNotAvailable: the requested model is not one of the agent's candidates.
	ErrModelNotAvailable = errors.New("model not available for this agent")
)

// SyncRunner runs a task as a sub-agent (a copy of the agent with full tools) and
// returns the worker's RAW content plus usage. Distinct from Spawner.Spawn's wait
// mode, which returns a file pointer for the LLM; SyncRunner is for programmatic
// hosts (e.g. an embedded orchestrator) that need the text. The same value
// injected as Deps.Spawn satisfies both interfaces. model is an optional model
// alias/name from the agent's candidates; "" runs the agent's default model.
type SyncRunner interface {
	RunSync(ctx context.Context, task, model string) (*SyncResult, error)
}
