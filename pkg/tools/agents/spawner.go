// ClawEh
// License: MIT

package agents

import (
	"context"
	"fmt"
	"strings"

	"github.com/PivotLLM/ClawEh/pkg/global"
	"github.com/PivotLLM/ClawEh/pkg/tools"
)

// Spawner is the robust sub-agent launcher injected via Deps.Spawn. It wraps a
// SubagentManager and exposes all three launch modes (detached / callback / wait)
// through the transport-neutral global.Spawner interface, so the internal spawn
// tool and any external/MCP tool launch workers through the same path.
type Spawner struct {
	mgr            *SubagentManager
	allowlistCheck func(targetAgentID string) bool
}

// Compile-time check: Spawner implements global.Spawner.
var _ global.Spawner = (*Spawner)(nil)

// NewSpawner builds a Spawner over the given manager.
func NewSpawner(mgr *SubagentManager) *Spawner {
	return &Spawner{mgr: mgr}
}

// SetAllowlistChecker installs the targeted-spawn authorization check. Self-spawns
// (empty TargetAgentID) are always allowed; targeted spawns must pass this check.
func (s *Spawner) SetAllowlistChecker(check func(targetAgentID string) bool) {
	s.allowlistCheck = check
}

// Spawn launches a worker per req.Mode. See global.Spawner.
func (s *Spawner) Spawn(ctx context.Context, req global.SpawnRequest) (*global.Result, error) {
	if s == nil || s.mgr == nil {
		return &global.Result{IsError: true, ForLLM: "spawn is not available"}, nil
	}
	if strings.TrimSpace(req.Task) == "" {
		return &global.Result{IsError: true, ForLLM: "task is required and must be a non-empty string"}, nil
	}

	// Authorize targeted spawns. Self-spawns are authorized by the caller already
	// holding the spawn capability.
	if s.allowlistCheck != nil && req.TargetAgentID != "" && !s.allowlistCheck(req.TargetAgentID) {
		return &global.Result{IsError: true, ForLLM: fmt.Sprintf("not allowed to spawn agent '%s'", req.TargetAgentID)}, nil
	}

	channel := req.Channel
	if channel == "" {
		channel = "cli"
	}
	chatID := req.ChatID
	if chatID == "" {
		chatID = "direct"
	}

	switch req.Mode {
	case global.SpawnAndWait:
		res, err := s.mgr.Run(ctx, req.Task, req.Label, req.TargetAgentID, channel, chatID)
		if err != nil {
			return &global.Result{IsError: true, ForLLM: fmt.Sprintf("subagent execution failed: %v", err)}, nil
		}
		return tools.ResultToGlobal(res), nil

	case global.SpawnCallback:
		var cb tools.AsyncCallback
		if req.OnResult != nil {
			onResult := req.OnResult
			cb = func(_ context.Context, r *tools.ToolResult) {
				onResult(tools.ResultToGlobal(r))
			}
		}
		msg, err := s.mgr.Spawn(ctx, req.Task, req.Label, req.TargetAgentID, channel, chatID, cb)
		if err != nil {
			return &global.Result{IsError: true, ForLLM: fmt.Sprintf("failed to spawn subagent: %v", err)}, nil
		}
		return tools.ResultToGlobal(tools.AsyncResult(msg)), nil

	default: // global.SpawnDetached
		msg, err := s.mgr.Spawn(ctx, req.Task, req.Label, req.TargetAgentID, channel, chatID, nil)
		if err != nil {
			return &global.Result{IsError: true, ForLLM: fmt.Sprintf("failed to spawn subagent: %v", err)}, nil
		}
		return tools.ResultToGlobal(tools.AsyncResult(msg)), nil
	}
}
