// ClawEh
// License: MIT

package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"sync"

	"github.com/PivotLLM/ClawEh/pkg/agenttoken"
	"github.com/PivotLLM/ClawEh/pkg/config"
	"github.com/PivotLLM/ClawEh/pkg/logger"
	"github.com/PivotLLM/ClawEh/pkg/mcpserver/acl"
	"github.com/PivotLLM/ClawEh/pkg/tools"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// messageToolName is the agent-internal outbound-publish tool, never exposed
// to MCP clients (it has no meaningful semantics outside the agent loop).
const messageToolName = "message"

// invalidTokenMessage is what we return when the supplied session_token is
// missing, malformed, or unknown. The wording is intentionally instructive
// so a confused LLM can self-correct on the next call.
const invalidTokenMessage = "invalid or missing session_token; supply your assigned token (format: SST<64 hex>)"

// subagentMessage is returned when the literal sub-agent sentinel is used.
const subagentMessage = "sub-agents are not granted claw MCP access; use the harness filesystem tools against your assigned working directory."

// aclDeniedMessage is returned when the per-agent ACL refuses a tool call
// for an otherwise-valid token. The wording avoids leaking why a specific
// (agent, tool) pair was denied.
const aclDeniedMessage = "agent not authorized for this tool"

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
// server. Every registered tool has the required `session_token` parameter
// added to its published schema. On every call:
//   - the session_token is extracted and resolved to an (agentID, sessionKey) pair;
//   - the per-agent ACL policy is consulted;
//   - for session-scoped tools the resolved session key is injected into ctx;
//   - the call is dispatched to that agent's tool registry;
//   - the result is scrubbed of any leaked tokens before return.
//
// Tool enumeration via tools/list is intentionally global — the catalogue
// is built from the union of every per-agent registry (deduped by name).
// tools/list never inspects the session_token. Per-agent restrictions are
// enforced at tools/call via the supplied acl.Policy.
func addToolsToServer(
	srv *server.MCPServer,
	agentRegistries map[string]*tools.ToolRegistry,
	allowPatterns []string,
	sessionTokens *sessionTokenStore,
	resolver AgentResolver,
	tracker *firstCallTracker,
	policy acl.Policy,
) {
	if policy == nil {
		policy = acl.Default
	}

	for _, name := range catalogueToolNames(agentRegistries) {
		if name == messageToolName {
			continue
		}
		if !config.MatchToolPattern(allowPatterns, name) {
			continue
		}

		tool, ok := firstToolNamed(agentRegistries, name)
		if !ok {
			continue
		}

		params := tool.Parameters()
		if params == nil {
			params = map[string]any{"type": "object", "properties": map[string]any{}}
		}
		// Every tool requires session_token — it identifies both agent and session.
		augmented := injectSessionTokenParam(params)

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
			out, isErr := dispatchToolCall(ctx, toolName, args, sessionTokens, resolver, tracker, policy)
			if isErr {
				return mcp.NewToolResultError(out), nil
			}
			return mcp.NewToolResultText(out), nil
		})

		logger.DebugCF("mcpserver", "registered tool",
			map[string]any{"tool": name})
	}
}

// catalogueToolNames returns the sorted union of tool names across every
// agent registry. Sorting keeps the boot log + tools/list output stable.
func catalogueToolNames(agentRegistries map[string]*tools.ToolRegistry) []string {
	seen := make(map[string]struct{})
	for _, reg := range agentRegistries {
		if reg == nil {
			continue
		}
		for _, name := range reg.List() {
			seen[name] = struct{}{}
		}
	}
	names := make([]string, 0, len(seen))
	for name := range seen {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// firstToolNamed returns any agent registry's instance of the named tool,
// used as the schema source for tools/list. Tool schemas are agnostic to
// the workspace they're bound to, so any instance is sufficient.
func firstToolNamed(agentRegistries map[string]*tools.ToolRegistry, name string) (tools.Tool, bool) {
	keys := make([]string, 0, len(agentRegistries))
	for k := range agentRegistries {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		reg := agentRegistries[k]
		if reg == nil {
			continue
		}
		if t, ok := reg.Get(name); ok {
			return t, true
		}
	}
	return nil, false
}

// dispatchToolCall validates the session_token in args, resolves the calling
// agent and session, consults the per-agent ACL policy, routes to the resolved
// agent's registry, executes the tool, and returns the (possibly redacted)
// LLM-facing output along with an error flag. For session-scoped tools the
// resolved session key is injected into the execution context.
//
// session_token is the sole auth mechanism: it maps to (agentID, sessionKey)
// server-side. agent_token is no longer used in MCP dispatch.
//
// Extracted so the dispatch logic can be exercised directly by tests without
// going through the streamable HTTP layer.
func dispatchToolCall(
	ctx context.Context,
	toolName string,
	args map[string]any,
	sessionTokens *sessionTokenStore,
	resolver AgentResolver,
	tracker *firstCallTracker,
	policy acl.Policy,
) (string, bool) {
	rawSessTok, _ := args[sessionTokenParam].(string)
	delete(args, sessionTokenParam)

	if agenttoken.IsSubagentSentinel(rawSessTok) {
		logger.WarnCF("mcpserver", "MCP token rejected: subagent sentinel",
			map[string]any{"tool": toolName, "reason": "subagent_sentinel"})
		return subagentMessage, true
	}

	if sessionTokens == nil || rawSessTok == "" {
		logger.WarnCF("mcpserver", "MCP token rejected",
			map[string]any{"tool": toolName, "reason": "invalid_token", "token_len": len(rawSessTok)})
		return invalidTokenMessage, true
	}

	rec, found := sessionTokens.Resolve(rawSessTok)
	if !found {
		logger.WarnCF("mcpserver", "MCP token rejected",
			map[string]any{"tool": toolName, "reason": "invalid_token", "token_len": len(rawSessTok)})
		return invalidTokenMessage, true
	}

	agentName := rec.agentID

	reg, ok := resolver(agentName)
	if !ok || reg == nil {
		logger.WarnCF("mcpserver", "MCP token rejected: no registry for agent",
			map[string]any{"tool": toolName, "agent": agentName, "reason": "no_registry"})
		return fmt.Sprintf("agent %q has no registered tool registry", agentName), true
	}

	if _, toolOK := reg.Get(toolName); !toolOK {
		logger.WarnCF("mcpserver", "MCP tool not in agent registry",
			map[string]any{"agent": agentName, "tool": toolName, "reason": "tool_not_in_registry"})
		return aclDeniedMessage, true
	}

	if policy == nil {
		policy = acl.Default
	}
	if !policy.IsAllowed(agentName, toolName) {
		logger.WarnCF("mcpserver", "MCP tool denied",
			map[string]any{"agent": agentName, "tool": toolName, "reason": "acl_denied"})
		return aclDeniedMessage, true
	}

	// Session-scoped tools call tools.ToolSessionKey(ctx); inject the resolved
	// session key so they retrieve the correct session regardless of HTTP state.
	if isSessionScopedTool(toolName) {
		ctx = tools.WithSessionKey(ctx, rec.sessionKey)
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

// injectSessionTokenParam returns a deep-copied schema with `session_token` added
// to properties and required. Called for every exposed tool — session_token is
// the sole auth mechanism for all mcp__claw__* calls. The original schema map
// is left untouched.
func injectSessionTokenParam(params map[string]any) map[string]any {
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
	props[sessionTokenParam] = map[string]any{
		"type":        "string",
		"description": "Your session_token (format: 'SST<64 hex>'). Required on every mcp__claw__* call. Supplied verbatim from the agent's system prompt.",
	}
	clone["properties"] = props

	required := stringSliceFromAny(clone["required"])
	if !containsString(required, sessionTokenParam) {
		required = append(required, sessionTokenParam)
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
