package agents

import (
	"strings"

	"github.com/PivotLLM/ClawEh/pkg/config"
	"github.com/PivotLLM/ClawEh/pkg/global"
)

// GlobalProvider exposes the subagent-spawn tool through the transport-neutral
// global layer with the BARE name "spawn". The aggregator mounts it under the
// "agent" namespace → published as "agent_spawn". The handler launches workers
// through the robust global.Spawner injected via Deps.Spawn, supporting three
// modes: detached (fire-and-forget), callback (fire-and-forget with the result
// re-injected onto the channel), and wait (run synchronously and return). The
// def is flagged Async so the bridge wires completion callbacks through
// ToolCall.Notify for the callback mode.
var GlobalProvider globalAgentProvider

type globalAgentProvider struct{}

func (globalAgentProvider) Namespace() string  { return "agent" }
func (globalAgentProvider) Description() string { return "Subagent spawning" }

// Available gates the package on the subagent capability being enabled, matching
// the old Build-time check. When disabled the host reports the tool as blocked
// with reason "requires_subagent".
func (globalAgentProvider) Available(cfg any) (bool, string) {
	c, ok := cfg.(*config.Config)
	if !ok || c == nil {
		return true, ""
	}
	if !c.Tools.IsToolEnabled("subagent") {
		return false, "requires_subagent"
	}
	return true, ""
}

const spawnToolDescription = "Spawn a subagent to handle a task. Use 'mode' to choose how it runs: " +
	"'callback' (default) runs it in the background and reports the result back when done; " +
	"'detached' runs it in the background and does not report back; " +
	"'wait' runs it to completion and returns the result immediately."

func spawnToolSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"task": map[string]any{
				"type":        "string",
				"description": "The task for the subagent to complete",
			},
			"label": map[string]any{
				"type":        "string",
				"description": "Optional short label for the task (for display)",
			},
			"agent_id": map[string]any{
				"type":        "string",
				"description": "Optional target agent ID to delegate the task to",
			},
			"mode": map[string]any{
				"type":        "string",
				"description": "How to run: 'callback' (default), 'detached', or 'wait'",
				"enum":        []string{"callback", "detached", "wait"},
			},
		},
		"required": []string{"task"},
	}
}

func (globalAgentProvider) RegisterTools(deps global.Deps) []global.ToolDefinition {
	// Recover the injected robust spawner. nil during deps-free enumeration; the
	// handler guards that case.
	sp, _ := deps.Spawn.(global.Spawner)

	return []global.ToolDefinition{
		{
			Name:        "spawn",
			Description: spawnToolDescription,
			RawSchema:   spawnToolSchema(),
			Category:    "agents",
			Async:       true,
			Handler: func(call *global.ToolCall) (*global.Result, error) {
				if sp == nil {
					return &global.Result{IsError: true, ForLLM: "spawn tool not available"}, nil
				}
				task, _ := call.Args["task"].(string)
				label, _ := call.Args["label"].(string)
				agentID, _ := call.Args["agent_id"].(string)
				modeStr, _ := call.Args["mode"].(string)

				mode, onResult := resolveSpawnMode(modeStr, call)
				return sp.Spawn(call.Ctx, global.SpawnRequest{
					Mode:          mode,
					Task:          task,
					Label:         label,
					TargetAgentID: agentID,
					Channel:       call.Channel,
					ChatID:        call.ChatID,
					OnResult:      onResult,
				})
			},
		},
	}
}

// resolveSpawnMode maps the tool's "mode" argument to a global.SpawnMode and, for
// callback mode, the OnResult sink that re-injects the worker's result onto the
// channel via ToolCall.Notify. Callback mode degrades to detached when the host
// offers no async path (Notify == nil).
func resolveSpawnMode(modeStr string, call *global.ToolCall) (global.SpawnMode, func(*global.Result)) {
	switch strings.ToLower(strings.TrimSpace(modeStr)) {
	case "wait", "sync", "synchronous":
		return global.SpawnAndWait, nil
	case "detached", "fire", "fire-and-forget":
		return global.SpawnDetached, nil
	default: // "callback", "", or unknown → prefer reporting back when possible
		if call.Notify != nil {
			return global.SpawnCallback, call.Notify
		}
		return global.SpawnDetached, nil
	}
}
