// ClawEh
// License: MIT

package mcpserver

import (
	"context"
	"encoding/json"

	"github.com/PivotLLM/ClawEh/pkg/config"
	"github.com/PivotLLM/ClawEh/pkg/logger"
	"github.com/mark3labs/mcp-go/mcp"
)

// messageToolName is the agent-internal outbound-publish tool, never exposed
// to MCP clients (it has no meaningful semantics outside the agent loop).
const messageToolName = "message"

// addTools registers each allowed claw tool with the MCP server.
// Tools are exposed iff they pass the allowlist AND are not the agent's
// internal "message" tool. The registry's existing schema (Parameters())
// is forwarded verbatim as the MCP input schema.
func (m *MCPServer) addTools() {
	for _, name := range m.registry.List() {
		if name == messageToolName {
			continue
		}
		if !config.MatchToolPattern(m.allowPatterns, name) {
			continue
		}

		tool, ok := m.registry.Get(name)
		if !ok {
			continue
		}

		params := tool.Parameters()
		if params == nil {
			params = map[string]any{"type": "object", "properties": map[string]any{}}
		}
		schemaBytes, err := json.Marshal(params)
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
		m.srv.AddTool(mcpTool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			args := req.GetArguments()
			if args == nil {
				args = map[string]any{}
			}

			result := m.registry.Execute(ctx, toolName, args)
			if result == nil {
				return mcp.NewToolResultError("tool returned nil result"), nil
			}

			if result.IsError {
				return mcp.NewToolResultError(result.ForLLM), nil
			}
			return mcp.NewToolResultText(result.ForLLM), nil
		})

		logger.DebugCF("mcpserver", "registered tool",
			map[string]any{"tool": name})
	}
}
