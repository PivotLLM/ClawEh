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
// SubagentManager and exposes the two launch modes (callback / wait) through the
// transport-neutral global.Spawner interface, plus task inspection
// (global.TaskInspector), so the internal spawn tool and any external/MCP tool
// launch and query workers through the same path.
type Spawner struct {
	mgr            *SubagentManager
	allowlistCheck func(targetAgentID string) bool
}

// Compile-time checks.
var (
	_ global.Spawner       = (*Spawner)(nil)
	_ global.TaskInspector = (*Spawner)(nil)
)

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

	// A requested model must be one of the executing agent's configured models
	// (self-spawn → the caller's; targeted → the target's). This is the security
	// boundary: an agent can only run a model it is already allowed to use.
	if strings.TrimSpace(req.Model) != "" {
		cands := s.mgr.CandidatesFor(req.TargetAgentID)
		if _, ok := MatchCandidate(cands, req.Model); !ok {
			return &global.Result{IsError: true, ForLLM: fmt.Sprintf(
				"model %q is not available for this agent; choose one of: %s",
				req.Model, candidateNames(cands))}, nil
		}
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
		res, err := s.mgr.Run(ctx, req.Task, req.Label, req.TargetAgentID, channel, chatID, req.Model)
		if err != nil {
			return &global.Result{IsError: true, ForLLM: fmt.Sprintf("subagent execution failed: %v", err)}, nil
		}
		return tools.ResultToGlobal(res), nil

	default: // global.SpawnCallback
		name := strings.TrimSpace(req.Name)
		if name == "" {
			name = strings.TrimSpace(req.Label)
		}
		if name == "" {
			return &global.Result{IsError: true, ForLLM: "name is required for callback mode (a short identifier for the task)"}, nil
		}
		var cb tools.AsyncCallback
		if req.OnResult != nil {
			onResult := req.OnResult
			cb = func(_ context.Context, r *tools.ToolResult) {
				onResult(tools.ResultToGlobal(r))
			}
		}
		id, err := s.mgr.SpawnCallback(req.Task, name, req.TargetAgentID, channel, chatID, req.Model, cb)
		if err != nil {
			return &global.Result{IsError: true, ForLLM: fmt.Sprintf("failed to spawn subagent: %v", err)}, nil
		}
		msg := fmt.Sprintf("Spawned background task '%s' (uuid: %s). It runs in the background; "+
			"you'll be notified on completion with a pointer to the result file, or poll agent_status with uuid=%q.",
			name, id, id)
		return tools.ResultToGlobal(tools.AsyncResult(msg)), nil
	}
}

// TaskStatus implements global.TaskInspector.
func (s *Spawner) TaskStatus(uuid string) (*global.TaskStatus, error) {
	if s == nil || s.mgr == nil {
		return nil, fmt.Errorf("spawn is not available")
	}
	return s.mgr.TaskStatus(uuid)
}

// TaskList implements global.TaskInspector.
func (s *Spawner) TaskList() ([]global.TaskBrief, error) {
	if s == nil || s.mgr == nil {
		return nil, fmt.Errorf("spawn is not available")
	}
	return s.mgr.TaskList()
}
