// ClawEh
// License: MIT

package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/PivotLLM/ClawEh/pkg/agenttoken"
	"github.com/PivotLLM/ClawEh/pkg/bus"
	"github.com/PivotLLM/ClawEh/pkg/config"
	"github.com/PivotLLM/ClawEh/pkg/logger"
	"github.com/PivotLLM/ClawEh/pkg/mcpserver/acl"
	"github.com/PivotLLM/ClawEh/pkg/routing"
	"github.com/PivotLLM/ClawEh/pkg/tools"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

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

// authMode describes how one endpoint authenticates tool calls. The two ClawEh
// MCP endpoints share registries, ACL, and token store and differ only here:
//   - /internal: tools carry an injected session_token parameter; the token
//     arrives in the call arguments (current behavior).
//   - /mcp: tools have clean schemas; the token is a standard Authorization:
//     Bearer header, placed in the request context by bearerContextFunc and
//     copied into the arguments here so dispatch is transport-agnostic.
//
// prepareArgs runs just before dispatch and guarantees the session_token is in
// args, regardless of transport; dispatchToolCall then reads and strips it.
type authMode struct {
	injectParam bool // add session_token to each published schema
	prepareArgs func(ctx context.Context, args map[string]any)
}

// internalAuthMode: the session_token is already an in-call parameter.
var internalAuthMode = authMode{
	injectParam: true,
	prepareArgs: func(context.Context, map[string]any) {},
}

// bearerAuthMode: copy the bearer token from context into args under the same
// key, so dispatch resolves it identically to the /internal parameter.
var bearerAuthMode = authMode{
	injectParam: false,
	prepareArgs: func(ctx context.Context, args map[string]any) {
		args[sessionTokenParam] = bearerTokenFromContext(ctx)
	},
}

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
	mode authMode,
	agentRegistries map[string]*tools.ToolRegistry,
	allowPatterns []string,
	sessionTokens *sessionTokenStore,
	resolver AgentResolver,
	tracker *firstCallTracker,
	policy acl.Policy,
	msgBus *bus.MessageBus,
	activeDispatches *atomic.Int32,
) {
	if policy == nil {
		policy = acl.Default
	}

	published := map[string]string{} // external name -> internal name (collision guard)
	for _, name := range catalogueToolNames(agentRegistries) {
		// msg_send (the outbound-message tool) obeys the allowlist like any other
		// tool: it is only reachable by an authenticated MCP client holding a valid
		// session token, and it can only post to that session's own user, so there
		// is no hard exclusion — include it in the allowlist to expose it.
		// Visibility is matched on the INTERNAL name (config semantics are unchanged
		// by the external renaming below).
		if !config.MatchVisibility(allowPatterns, name) {
			continue
		}

		tool, ok := firstToolNamed(agentRegistries, name)
		if !ok {
			continue
		}

		// External catalogue name: upstream-MCP tools self-name via ExternalNamer
		// (e.g. "<server>_<tool>", stripping the internal "mcp_" prefix); every
		// other tool is published under its own registry name (no prefix). Any
		// collision between two external names is caught by the dedupe guard below.
		pubName := name
		if en, ok := tool.(tools.ExternalNamer); ok {
			pubName = en.ExternalName()
		}
		if prior, dup := published[pubName]; dup {
			logger.WarnCF("mcpserver", "skipping tool: external name collision",
				map[string]any{"external": pubName, "tool": name, "conflicts_with": prior})
			continue
		}
		published[pubName] = name

		params := tool.Parameters()
		if params == nil {
			params = map[string]any{"type": "object", "properties": map[string]any{}}
		}
		// On /internal the session_token is an in-call parameter (it identifies both
		// agent and session); on /mcp the token is the bearer header, so schemas stay
		// clean.
		schema := params
		if mode.injectParam {
			schema = injectSessionTokenParam(params)
		}

		schemaBytes, err := json.Marshal(schema)
		if err != nil {
			logger.WarnCF("mcpserver", "skipping tool: failed to marshal schema",
				map[string]any{"tool": name, "error": err.Error()})
			continue
		}

		// NewToolWithRawSchema is required when supplying a raw JSON schema —
		// NewTool initializes an empty InputSchema, and the marshaller refuses
		// to serialize a Tool with both InputSchema and RawInputSchema set.
		// Published under the external name; dispatch still resolves the internal one.
		mcpTool := mcp.NewToolWithRawSchema(pubName, tool.Description(), schemaBytes)

		toolName := name // capture the INTERNAL name for dispatch
		srv.AddTool(mcpTool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			if activeDispatches != nil {
				activeDispatches.Add(1)
				defer activeDispatches.Add(-1)
			}
			args := req.GetArguments()
			if args == nil {
				args = map[string]any{}
			}
			mode.prepareArgs(ctx, args)
			out, isErr := dispatchToolCall(ctx, toolName, args, sessionTokens, resolver, tracker, policy, msgBus)
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

// dispatchToolCall validates the session_token in args (placed there by the
// endpoint's authMode — the in-call parameter on /internal, or the bearer header
// copied in on /mcp), resolves the calling agent and session, consults the
// per-agent ACL policy, routes to the resolved agent's registry, executes the
// tool, and returns the (possibly redacted) LLM-facing output along with an
// error flag. For session-scoped tools the resolved session key is injected into
// the execution context.
//
// The session token is the sole auth mechanism: it maps to (agentID, sessionKey)
// server-side regardless of transport. agent_token is no longer used in MCP
// dispatch.
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
	msgBus *bus.MessageBus,
) (string, bool) {
	rawSessTok, _ := args[sessionTokenParam].(string)
	delete(args, sessionTokenParam)

	if agenttoken.IsSubagentSentinel(rawSessTok) {
		logger.WarnCF("mcpserver", "MCP token rejected: subagent sentinel",
			map[string]any{"tool": toolName, "reason": "subagent_sentinel"})
		return subagentMessage, true
	}

	if rawSessTok == "" {
		logger.WarnCF("mcpserver", "MCP tool call presented no session_token",
			map[string]any{"tool": toolName, "reason": "no_token"})
		return invalidTokenMessage, true
	}
	if sessionTokens == nil {
		logger.WarnCF("mcpserver", "MCP token rejected: token store unavailable",
			map[string]any{"tool": toolName, "reason": "no_token_store"})
		return invalidTokenMessage, true
	}

	rec, found := sessionTokens.Resolve(rawSessTok)
	if !found {
		logger.WarnCF("mcpserver", "MCP session token verification failed",
			map[string]any{"tool": toolName, "reason": "invalid_token", "token_len": len(rawSessTok)})
		return invalidTokenMessage, true
	}

	agentName := rec.agentID
	logger.InfoCF("mcpserver", "MCP session token verified",
		map[string]any{"agent": agentName, "session": rec.sessionKey, "tool": toolName})

	reg, ok := resolver(agentName)
	if !ok || reg == nil {
		logger.WarnCF("mcpserver", "MCP token rejected: no registry for agent",
			map[string]any{"tool": toolName, "agent": agentName, "reason": "no_registry"})
		return fmt.Sprintf("agent %q has no registered tool registry", agentName), true
	}

	toolInstance, toolOK := reg.Get(toolName)
	if !toolOK {
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

	// A sub-agent session (e.g. a CLI provider reaching tools over MCP) must never
	// run a PrimaryOnly tool — this closes the recursion / privilege hole that the
	// in-loop registry filter covers for API providers.
	if routing.IsSubagentSessionKey(rec.sessionKey) && tools.IsPrimaryOnly(toolInstance) {
		logger.WarnCF("mcpserver", "MCP tool denied for sub-agent",
			map[string]any{"agent": agentName, "tool": toolName, "reason": "primary_only"})
		return fmt.Sprintf("tool %q is not available to sub-agents", toolName), true
	}

	// Session-scoped tools call tools.ToolSessionKey(ctx); inject the resolved
	// session key so they retrieve the correct session regardless of HTTP state.
	if t, ok := toolInstance.(tools.SessionScoped); ok && t.IsSessionScoped() {
		ctx = tools.WithSessionKey(ctx, rec.sessionKey)
	}

	// Carry the session's source channel/chatID so tools that re-inject a turn
	// (e.g. session_clear) can route the follow-up back to the originating user
	// on the MCP path, which otherwise has no channel/chatID in context.
	ctx = tools.WithToolContext(ctx, rec.channel, rec.chatID)

	logger.InfoCF("mcpserver", "MCP tool authorized",
		map[string]any{"agent": agentName, "tool": toolName, "workspace": tracker.workspace(agentName)})

	if tracker != nil {
		tracker.record(agentName)
	}

	// Async completion handler: when a background tool (e.g. agent_spawn in
	// callback mode) finishes later, deliver its result the same way the agent
	// loop does — ForUser to the user, and ForLLM re-injected as a "system"
	// message into the agent's session — so the primary LLM is notified without
	// polling, for CLI and non-CLI providers alike. (The immediate/sync result is
	// handled below.)
	asyncCb := func(_ context.Context, r *tools.ToolResult) {
		publishMCPForUser(context.Background(), msgBus, rec, toolName, r)
		publishMCPAsyncToLLM(msgBus, rec, toolName, r)
	}
	result := reg.ExecuteWithContext(ctx, toolName, args, rec.channel, rec.chatID, asyncCb)
	if result == nil {
		logger.WarnCF("mcpserver", "tool returned nil result",
			map[string]any{"tool": toolName, "agent": agentName, "reason": "nil_result"})
		return agenttoken.Redact("tool returned nil result"), true
	}

	// Side-channel publish of the immediate ForUser to the originating user. The
	// MCP response envelope to the caller carries only ForLLM; ForUser is delivered
	// out-of-band over the message bus to the session's recorded inbound source.
	// (Async completions are handled by asyncCb above.)
	publishMCPForUser(ctx, msgBus, rec, toolName, result)

	out := agenttoken.Redact(result.ForLLM)
	return out, result.IsError
}

// publishMCPAsyncToLLM re-injects an async tool's completion (ForLLM, or the
// error) into the agent's session as a "system" message, so the primary LLM is
// notified of the result without polling. Mirrors the agent-loop async path
// (pkg/agent/loop.go). No-op when there is nothing to inject; when there is no
// recorded channel source to route to, it logs the drop rather than failing
// silently.
func publishMCPAsyncToLLM(msgBus *bus.MessageBus, rec sessionRecord, toolName string, r *tools.ToolResult) {
	if r == nil || msgBus == nil {
		return
	}
	content := r.ForLLM
	if content == "" && r.Err != nil {
		content = r.Err.Error()
	}
	if content == "" {
		return
	}
	if rec.channel == "" || rec.chatID == "" {
		// No recorded channel for this session, so we cannot route the re-injected
		// completion. Log it rather than dropping silently — a headless/pure-MCP
		// agent that never processed a channel message will not be notified.
		logger.WarnCF("mcpserver", "mcp.async.reinject_dropped",
			map[string]any{
				"tool":        toolName,
				"agent":       rec.agentID,
				"session_key": rec.sessionKey,
				"reason":      "no_active_channel",
			})
		return
	}
	pubCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := msgBus.PublishInbound(pubCtx, bus.InboundMessage{
		Channel:    "system",
		SenderID:   fmt.Sprintf("async:%s", toolName),
		ChatID:     fmt.Sprintf("%s:%s", rec.channel, rec.chatID),
		Content:    content,
		SessionKey: rec.sessionKey,
		Metadata:   map[string]string{"preresolved_agent_id": rec.agentID},
	}); err != nil {
		logger.WarnCF("mcpserver", "mcp.async.reinject_failed",
			map[string]any{"tool": toolName, "agent": rec.agentID, "error": err.Error()})
		return
	}
	logger.InfoCF("mcpserver", "mcp.async.reinject",
		map[string]any{
			"tool":        toolName,
			"agent":       rec.agentID,
			"session_key": rec.sessionKey,
			"channel":     rec.channel,
			"content_len": len(content),
		})
}

// publishMCPForUser sends a tool's ForUser payload to the originating user's
// channel/chatID via the outbound message bus, when a payload exists, the
// result is not Silent, and the session has a recorded source. Silent drop in
// every other case — the MCP caller's ForLLM response is unaffected.
func publishMCPForUser(
	ctx context.Context,
	msgBus *bus.MessageBus,
	rec sessionRecord,
	toolName string,
	result *tools.ToolResult,
) {
	if result == nil || result.Silent || result.ForUser == "" {
		return
	}
	if msgBus == nil {
		logger.WarnCF("mcpserver", "mcp.foruser.dropped",
			map[string]any{
				"tool":        toolName,
				"agent":       rec.agentID,
				"session_key": rec.sessionKey,
				"reason":      "no_bus",
			})
		return
	}
	if rec.channel == "" || rec.chatID == "" {
		logger.WarnCF("mcpserver", "mcp.foruser.dropped",
			map[string]any{
				"tool":        toolName,
				"agent":       rec.agentID,
				"session_key": rec.sessionKey,
				"reason":      "no_active_channel",
			})
		return
	}

	pubCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := msgBus.PublishOutbound(pubCtx, bus.OutboundMessage{
		Channel: rec.channel,
		ChatID:  rec.chatID,
		Content: result.ForUser,
	}); err != nil {
		logger.WarnCF("mcpserver", "mcp.foruser.publish_failed",
			map[string]any{
				"tool":        toolName,
				"agent":       rec.agentID,
				"session_key": rec.sessionKey,
				"channel":     rec.channel,
				"error":       err.Error(),
			})
		return
	}
	logger.InfoCF("mcpserver", "mcp.foruser.delivered",
		map[string]any{
			"tool":        toolName,
			"agent":       rec.agentID,
			"session_key": rec.sessionKey,
			"channel":     rec.channel,
			"content_len": len(result.ForUser),
		})
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
