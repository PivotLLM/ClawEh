// ClawEh
// License: MIT

package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/PivotLLM/ClawEh/pkg/agenttoken"
	"github.com/PivotLLM/ClawEh/pkg/config"
	"github.com/PivotLLM/ClawEh/pkg/logger"
	"github.com/PivotLLM/ClawEh/pkg/tools"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// messageToolName is the agent-internal outbound-publish tool, never exposed
// to MCP clients (it has no meaningful semantics outside the agent loop).
const messageToolName = "message"

// agentTokenParam is the snake_case parameter name every mcp__claw__* tool
// requires from its caller. The MCP server strips it before dispatching to
// the underlying tool implementation.
const agentTokenParam = "agent_token"

// invalidTokenMessage is what we return when the supplied agent_token is
// missing, malformed, or unknown. The wording is intentionally instructive
// so a confused LLM can self-correct on the next call.
const invalidTokenMessage = "invalid or missing agent_token; supply your assigned token (format: AGT<64 hex>)"

// subagentMessage is returned when the literal sub-agent sentinel is used.
const subagentMessage = "sub-agents are not granted claw MCP access; use the harness filesystem tools against your assigned working directory."

// AgentResolver returns the per-agent tool registry for a given agent name,
// or (nil, false) when the agent is unknown.
type AgentResolver func(agentName string) (*tools.ToolRegistry, bool)

// firstCallTracker debounces "first MCP call from agent=<name>" log entries
// so we emit at most one per agent per server lifetime.
type firstCallTracker struct {
	mu    sync.Mutex
	seen  map[string]bool
	known map[string]string // agentName -> workspace (for the boot-log message)
}

func newFirstCallTracker(workspaces map[string]string) *firstCallTracker {
	known := make(map[string]string, len(workspaces))
	for k, v := range workspaces {
		known[k] = v
	}
	return &firstCallTracker{
		seen:  make(map[string]bool),
		known: known,
	}
}

func (t *firstCallTracker) record(agentName string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.seen[agentName] {
		return
	}
	t.seen[agentName] = true
	logger.InfoCF("mcpserver", "MCP call from agent",
		map[string]any{
			"agent":     agentName,
			"workspace": t.known[agentName],
		})
}

// workspace returns the recorded workspace for agentName, or "" if none was
// supplied at construction. Safe for concurrent use; the known map is
// effectively read-only after newFirstCallTracker.
func (t *firstCallTracker) workspace(agentName string) string {
	if t == nil {
		return ""
	}
	return t.known[agentName]
}

// addToolsToServer registers each allowed claw tool with the given MCP
// server. Each registered tool has the required `agent_token` parameter
// added to its published schema. On every call:
//   - the token is extracted, validated, and resolved to an agent name;
//   - the call is dispatched to that agent's tool registry;
//   - the result is scrubbed of any leaked AGT tokens before return.
//
// `defaultRegistry` is used only as the source of which tools to publish
// (names, descriptions, schemas). It is never used to execute tool calls.
func addToolsToServer(
	srv *server.MCPServer,
	defaultRegistry *tools.ToolRegistry,
	allowPatterns []string,
	tokens *agenttoken.Manager,
	resolver AgentResolver,
	tracker *firstCallTracker,
) {
	for _, name := range defaultRegistry.List() {
		if name == messageToolName {
			continue
		}
		if !config.MatchToolPattern(allowPatterns, name) {
			continue
		}

		tool, ok := defaultRegistry.Get(name)
		if !ok {
			continue
		}

		params := tool.Parameters()
		if params == nil {
			params = map[string]any{"type": "object", "properties": map[string]any{}}
		}
		augmented := injectAgentTokenParam(params)

		schemaBytes, err := json.Marshal(augmented)
		if err != nil {
			logger.WarnCF("mcpserver", "skipping tool: failed to marshal schema",
				map[string]any{"tool": name, "error": err.Error()})
			continue
		}

		// NewToolWithRawSchema is required when supplying a raw JSON schema —
		// NewTool initializes an empty InputSchema, and the marshaller refuses
		// to serialize a Tool with both InputSchema and RawInputSchema set.
		mcpTool := mcp.NewToolWithRawSchema(name, tool.Description(), schemaBytes)

		toolName := name // capture
		srv.AddTool(mcpTool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			args := req.GetArguments()
			if args == nil {
				args = map[string]any{}
			}
			out, isErr := dispatchToolCall(ctx, toolName, args, tokens, resolver, tracker)
			if isErr {
				return mcp.NewToolResultError(out), nil
			}
			return mcp.NewToolResultText(out), nil
		})

		logger.DebugCF("mcpserver", "registered tool",
			map[string]any{"tool": name})
	}
}

// dispatchToolCall validates the token in args, routes to the resolved
// agent's registry, executes the tool, and returns the (possibly redacted)
// LLM-facing output along with an error flag. Extracted so the dispatch
// logic can be exercised directly by tests without going through the
// streamable HTTP layer.
func dispatchToolCall(
	ctx context.Context,
	toolName string,
	args map[string]any,
	tokens *agenttoken.Manager,
	resolver AgentResolver,
	tracker *firstCallTracker,
) (string, bool) {
	rawTok, _ := args[agentTokenParam].(string)
	delete(args, agentTokenParam)

	if agenttoken.IsSubagentSentinel(rawTok) {
		logger.WarnCF("mcpserver", "MCP token rejected: subagent sentinel",
			map[string]any{"tool": toolName, "reason": "subagent_sentinel"})
		return subagentMessage, true
	}

	agentName, ok := tokens.Resolve(rawTok)
	if !ok {
		logger.WarnCF("mcpserver", "MCP token rejected",
			map[string]any{"tool": toolName, "reason": "invalid_token", "token_len": len(rawTok)})
		return invalidTokenMessage, true
	}

	reg, ok := resolver(agentName)
	if !ok || reg == nil {
		logger.WarnCF("mcpserver", "MCP token rejected: no registry for agent",
			map[string]any{"tool": toolName, "agent": agentName, "reason": "no_registry"})
		return fmt.Sprintf("agent %q has no registered tool registry", agentName), true
	}

	logger.InfoCF("mcpserver", "MCP tool authorized",
		map[string]any{"agent": agentName, "tool": toolName, "workspace": tracker.workspace(agentName)})

	if tracker != nil {
		tracker.record(agentName)
	}

	result := reg.Execute(ctx, toolName, args)
	if result == nil {
		logger.WarnCF("mcpserver", "tool returned nil result",
			map[string]any{"tool": toolName, "agent": agentName, "reason": "nil_result"})
		return agenttoken.Redact("tool returned nil result"), true
	}

	out := agenttoken.Redact(result.ForLLM)
	return out, result.IsError
}

// injectAgentTokenParam returns a deep-copied schema with `agent_token` added
// to properties and required. The original schema map is left untouched.
func injectAgentTokenParam(params map[string]any) map[string]any {
	clone := cloneMap(params)
	if clone == nil {
		clone = map[string]any{}
	}
	if _, ok := clone["type"]; !ok {
		clone["type"] = "object"
	}

	props, _ := clone["properties"].(map[string]any)
	if props == nil {
		props = map[string]any{}
	} else {
		props = cloneMap(props)
	}
	props[agentTokenParam] = map[string]any{
		"type":        "string",
		"description": "Your agent_token (format: 'AGT<64 hex>'). Required. Supplied verbatim from the agent's system prompt.",
	}
	clone["properties"] = props

	required := stringSliceFromAny(clone["required"])
	if !containsString(required, agentTokenParam) {
		required = append(required, agentTokenParam)
	}
	clone["required"] = required

	return clone
}

func cloneMap(m map[string]any) map[string]any {
	if m == nil {
		return nil
	}
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

func stringSliceFromAny(v any) []string {
	switch raw := v.(type) {
	case []string:
		return append([]string{}, raw...)
	case []any:
		out := make([]string, 0, len(raw))
		for _, item := range raw {
			if s, ok := item.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

func containsString(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}
