// ClawEh
// License: MIT

package agents

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/PivotLLM/ClawEh/global"
	"github.com/PivotLLM/ClawEh/tools"
)

// Spawner is the robust sub-agent launcher injected via Deps.Spawn. It wraps a
// SubagentManager and exposes the two launch modes (callback / wait) through the
// transport-neutral global.Spawner interface, plus task inspection
// (global.TaskInspector), so the internal spawn tool and any external/MCP tool
// launch and query workers through the same path.
type Spawner struct {
	mgr            *SubagentManager
	allowlistCheck func(targetAgentID string) bool
	// cardExists, when set, reports whether a character card exists in the
	// library, so a spawn on a missing card is refused with the card's name
	// before anything is created.
	cardExists func(id string) bool
	// maxDepth is the sub-agent recursion bound; 0 falls back to
	// DefaultMaxSpawnDepth (see maxSpawnDepth).
	maxDepth int
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

// SetCardChecker installs the character-card existence check used for
// SpawnRequest.CharacterCard.
func (s *Spawner) SetCardChecker(exists func(id string) bool) { s.cardExists = exists }

// SetMaxDepth sets the sub-agent recursion bound (agents.defaults.max_subagent_depth).
// A value <= 0 restores the default (DefaultMaxSpawnDepth).
func (s *Spawner) SetMaxDepth(d int) { s.maxDepth = d }

// maxSpawnDepth returns the effective recursion bound.
func (s *Spawner) maxSpawnDepth() int {
	if s.maxDepth > 0 {
		return s.maxDepth
	}
	return DefaultMaxSpawnDepth
}

// Spawn launches a worker per req.Mode. See global.Spawner.
func (s *Spawner) Spawn(ctx context.Context, req global.SpawnRequest) (*global.Result, error) {
	if s == nil || s.mgr == nil {
		return &global.Result{IsError: true, ForLLM: "spawn is not available"}, nil
	}
	// Bound recursion: a sub-agent already at the depth bound may not spawn
	// deeper, nor call a task model.
	if limit := s.maxSpawnDepth(); SpawnDepth(ctx) >= limit {
		return &global.Result{IsError: true, ForLLM: fmt.Sprintf(
			"maximum sub-agent depth (%d) reached; this task is already nested %d level(s) deep and cannot spawn further sub-agents",
			limit, SpawnDepth(ctx))}, nil
	}

	// A kind makes it a task-model call: no agent, no tools, no depth level.
	if strings.TrimSpace(req.Kind) != "" {
		return s.spawnTask(ctx, req)
	}
	if len(req.Files) > 0 || req.Voice != "" || req.Format != "" {
		return &global.Result{IsError: true, ForLLM: "files, voice and format are for task models: give kind too."}, nil
	}
	if strings.TrimSpace(req.Task) == "" {
		return &global.Result{IsError: true, ForLLM: "task is required and must be a non-empty string"}, nil
	}

	// Authorize targeted spawns. Self-spawns are authorized by the caller already
	// holding the spawn capability.
	if s.allowlistCheck != nil && req.TargetAgentID != "" && !s.allowlistCheck(req.TargetAgentID) {
		return &global.Result{IsError: true, ForLLM: fmt.Sprintf("not allowed to spawn agent '%s'", req.TargetAgentID)}, nil
	}

	// The worker runs on the executing agent's worker models (self-spawn → the
	// caller's; targeted → the target's).
	if problem := s.workerModelProblem(req.TargetAgentID, req.Model); problem != "" {
		return &global.Result{IsError: true, ForLLM: problem}, nil
	}

	// A character card must exist; the worker is created with it rendered.
	if card := strings.TrimSpace(req.CharacterCard); card != "" && s.cardExists != nil && !s.cardExists(card) {
		return &global.Result{IsError: true, ForLLM: fmt.Sprintf("Character card %q does not exist.", card)}, nil
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
		res, err := s.mgr.Run(ctx, req.Task, req.Label, req.TargetAgentID, channel, chatID, req.Model, strings.TrimSpace(req.CharacterCard), req.Media)
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
		id, err := s.mgr.SpawnCallback(req.Task, name, req.TargetAgentID, channel, chatID, req.Model, strings.TrimSpace(req.CharacterCard), req.Media, cb, SpawnDepth(ctx)) //nolint:contextcheck // callback-mode sub-agents run detached from the spawning turn by design; their lifetime is the task record
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
		return nil, errors.New("spawn is not available")
	}
	return s.mgr.TaskStatus(uuid)
}

// TaskList implements global.TaskInspector.
func (s *Spawner) TaskList() ([]global.TaskBrief, error) {
	if s == nil || s.mgr == nil {
		return nil, errors.New("spawn is not available")
	}
	return s.mgr.TaskList()
}

// RunSync runs a task as a self-spawned sub-agent and returns the worker's raw
// content. Implements global.SyncRunner for embedded orchestrators (e.g. Maestro)
// that dispatch task workers and consume the text directly.
func (s *Spawner) RunSync(ctx context.Context, task, model string) (*global.SyncResult, error) {
	if s == nil || s.mgr == nil {
		return nil, global.ErrSpawnUnavailable
	}
	// Bound recursion: a Maestro worker that is itself already at the depth bound
	// may not dispatch a further layer of workers.
	if limit := s.maxSpawnDepth(); SpawnDepth(ctx) >= limit {
		return nil, fmt.Errorf("%w: maximum sub-agent depth (%d) reached; cannot dispatch further sub-agents", global.ErrSpawnDepthExceeded, limit)
	}
	// Same check as Spawn, for a self-spawn.
	if problem := s.workerModelProblem("", model); problem != "" {
		return nil, fmt.Errorf("%w: %s", global.ErrModelNotAvailable, problem)
	}
	return s.mgr.RunSync(ctx, task, "", model)
}

// workerModelProblem says why a spawn of agentID (the caller for a
// self-spawn) cannot run: the agent has configured worker models and none is
// available, or the requested model is not one of its worker models. Empty
// when it can. This is the security boundary: an agent's worker only runs a
// model the agent is configured to give it.
func (s *Spawner) workerModelProblem(agentID, model string) string {
	cands := s.mgr.CandidatesFor(agentID)
	if len(cands) == 0 && s.mgr.hasWorkerModels(agentID) {
		return fmt.Sprintf("no worker model is available for agent %q; check its worker models", s.mgr.targetAgent(agentID))
	}
	if strings.TrimSpace(model) == "" {
		return ""
	}
	if _, ok := MatchCandidate(cands, model); !ok {
		return fmt.Sprintf("model %q is not available for agent %q; choose one of its worker models: %s",
			model, s.mgr.targetAgent(agentID), candidateNames(cands))
	}
	return ""
}
