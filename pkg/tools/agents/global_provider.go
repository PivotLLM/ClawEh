package agents

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/PivotLLM/ClawEh/pkg/config"
	"github.com/PivotLLM/ClawEh/pkg/global"
)

// GlobalProvider exposes the subagent task tools through the transport-neutral
// global layer under the "agent" namespace: bare "spawn" → "agent_spawn", bare
// "status" → "agent_status", bare "list" → "agent_list". The spawn handler
// launches workers through the robust global.Spawner injected via Deps.Spawn,
// supporting two modes: callback (background, file-backed, tracked) and wait (run
// synchronously and return). status/list query tracked callback tasks through
// global.TaskInspector. The spawn def is flagged Async so the bridge wires the
// completion pointer through ToolCall.Notify for callback mode.
var GlobalProvider globalAgentProvider

type globalAgentProvider struct{}

func (globalAgentProvider) Namespace() string   { return "agent" }
func (globalAgentProvider) Description() string { return "Subagent spawning and task tracking" }

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
	"'callback' (default) runs it in the background and notifies you when done with a pointer to a result file " +
	"in your workspace (read it with your file tools, or poll agent_status); 'wait' runs it to completion and " +
	"returns the result immediately. Callback mode requires 'name', a short identifier for the task."

func spawnToolSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"task": map[string]any{
				"type":        "string",
				"description": "The task for the subagent to complete",
			},
			"mode": map[string]any{
				"type":        "string",
				"description": "How to run: 'callback' (default, background) or 'wait' (synchronous)",
				"enum":        []string{"callback", "wait"},
			},
			"name": map[string]any{
				"type":        "string",
				"description": "Short identifier for the task (required for callback mode)",
			},
			"agent_id": map[string]any{
				"type":        "string",
				"description": "Optional target agent ID to delegate the task to",
			},
			"model": map[string]any{
				"type":        "string",
				"description": "Optional model for the subagent to run, by its model name. Must be one of the configured models for the executing agent (yourself on a self-spawn, or the target agent). Omit to use that agent's default model. Useful to run a heavier model for a demanding subtask while you stay on a lighter one.",
			},
		},
		"required": []string{"task"},
	}
}

func statusToolSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"uuid": map[string]any{
				"type":        "string",
				"description": "The task uuid returned by agent_spawn (or shown by agent_list)",
			},
		},
		"required": []string{"uuid"},
	}
}

func listToolSchema() map[string]any {
	return map[string]any{
		"type":       "object",
		"properties": map[string]any{},
	}
}

func (globalAgentProvider) RegisterTools(deps global.Deps) []global.ToolDefinition {
	// Recover the injected robust spawner. nil during deps-free enumeration; the
	// handlers guard that case.
	sp, _ := deps.Spawn.(global.Spawner)
	inspector, _ := deps.Spawn.(global.TaskInspector)

	return []global.ToolDefinition{
		{
			Name:        "spawn",
			Description: spawnToolDescription,
			RawSchema:   spawnToolSchema(),
			Category:    "agents",
			Async:       true,
			PrimaryOnly: true, // a sub-agent cannot spawn further sub-agents (no recursion)
			Handler: func(call *global.ToolCall) (*global.Result, error) {
				if sp == nil {
					return &global.Result{IsError: true, ForLLM: "spawn tool not available"}, nil
				}
				task, _ := call.Args["task"].(string)
				name, _ := call.Args["name"].(string)
				agentID, _ := call.Args["agent_id"].(string)
				modeStr, _ := call.Args["mode"].(string)
				model, _ := call.Args["model"].(string)

				mode, onResult := resolveSpawnMode(modeStr, call)
				return sp.Spawn(call.Ctx, global.SpawnRequest{
					Mode:          mode,
					Task:          task,
					Name:          name,
					Label:         name,
					TargetAgentID: agentID,
					Model:         model,
					Channel:       call.Channel,
					ChatID:        call.ChatID,
					OnResult:      onResult,
				})
			},
		},
		{
			Name:        "status",
			Description: "Check the status of a background task by uuid. Returns one of: unknown, running, done, error — with a pointer to the result file when available.",
			RawSchema:   statusToolSchema(),
			Category:    "agents",
			Handler: func(call *global.ToolCall) (*global.Result, error) {
				if inspector == nil {
					return &global.Result{IsError: true, ForLLM: "task status not available"}, nil
				}
				id, _ := call.Args["uuid"].(string)
				if strings.TrimSpace(id) == "" {
					return &global.Result{IsError: true, ForLLM: "uuid is required"}, nil
				}
				st, err := inspector.TaskStatus(id)
				if err != nil {
					return &global.Result{IsError: true, ForLLM: fmt.Sprintf("status lookup failed: %v", err)}, nil
				}
				b, _ := json.Marshal(st)
				return &global.Result{ForLLM: string(b), Silent: true}, nil
			},
		},
		{
			Name:        "list",
			Description: "List this agent's background tasks. Returns uuid, name, and status for each.",
			RawSchema:   listToolSchema(),
			Category:    "agents",
			Handler: func(call *global.ToolCall) (*global.Result, error) {
				if inspector == nil {
					return &global.Result{IsError: true, ForLLM: "task list not available"}, nil
				}
				list, err := inspector.TaskList()
				if err != nil {
					return &global.Result{IsError: true, ForLLM: fmt.Sprintf("task list failed: %v", err)}, nil
				}
				b, _ := json.Marshal(map[string]any{"tasks": list})
				return &global.Result{ForLLM: string(b), Silent: true}, nil
			},
		},
	}
}

// resolveSpawnMode maps the tool's "mode" argument to a global.SpawnMode and, for
// callback mode, the OnResult sink that delivers the worker's completion pointer
// (via ToolCall.Notify on the agent-loop path). When no async path is offered
// (Notify == nil, e.g. MCP), callback mode still runs and tracks the task — the
// result is retrieved via agent_status / the result file — but no push fires.
func resolveSpawnMode(modeStr string, call *global.ToolCall) (global.SpawnMode, func(*global.Result)) {
	switch strings.ToLower(strings.TrimSpace(modeStr)) {
	case "wait", "sync", "synchronous":
		return global.SpawnAndWait, nil
	default: // "callback", "", or unknown → background, tracked
		return global.SpawnCallback, call.Notify
	}
}
