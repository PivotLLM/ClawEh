package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
)

// MCPManager defines the interface for MCP manager operations
// This allows for easier testing with mock implementations
type MCPManager interface {
	CallTool(
		ctx context.Context,
		serverName, toolName string,
		arguments map[string]any,
	) (*mcp.CallToolResult, error)
}

// ExternalNamer is implemented by tools that should be advertised to external MCP
// clients under a different name than their internal registry name. The MCP host
// asks the tool for this name structurally rather than inspecting name prefixes,
// so the published catalogue reads cleanly (e.g. "<server>_<tool>") while internal
// gating and dispatch keep using the registry name.
type ExternalNamer interface {
	ExternalName() string
}

// MCPTool wraps an MCP tool to implement the Tool interface
type MCPTool struct {
	manager    MCPManager
	serverName string
	tool       mcp.Tool
}

// NewMCPTool creates a new MCP tool wrapper
func NewMCPTool(manager MCPManager, serverName string, tool mcp.Tool) *MCPTool {
	return &MCPTool{
		manager:    manager,
		serverName: serverName,
		tool:       tool,
	}
}

// sanitizeIdentifierComponent normalizes a string so it can be safely used
// as part of a tool/function identifier for downstream providers.
// It:
//   - lowercases the string
//   - replaces any character not in [a-z0-9_-] with '_'
//   - collapses multiple consecutive '_' into a single '_'
//   - trims leading/trailing '_'
//   - falls back to "unnamed" if the result is empty
//   - truncates overly long components to a reasonable length
func sanitizeIdentifierComponent(s string) string {
	const maxLen = 64

	s = strings.ToLower(s)
	var b strings.Builder
	b.Grow(len(s))

	prevUnderscore := false
	for _, r := range s {
		isAllowed := (r >= 'a' && r <= 'z') ||
			(r >= '0' && r <= '9') ||
			r == '_' || r == '-'

		if !isAllowed {
			// Normalize any disallowed character to '_'
			if !prevUnderscore {
				b.WriteRune('_')
				prevUnderscore = true
			}
			continue
		}

		if r == '_' {
			if prevUnderscore {
				continue
			}
			prevUnderscore = true
		} else {
			prevUnderscore = false
		}

		b.WriteRune(r)
	}

	result := strings.Trim(b.String(), "_")
	if result == "" {
		result = "unnamed"
	}

	if len(result) > maxLen {
		result = result[:maxLen]
	}

	return result
}

// MCPServerPattern returns the wildcard allowlist pattern that matches all
// tools from the named MCP server (e.g. "mcp_myserver_*"). Use this in an
// agent's tools list to grant access to every tool on a server at once.
func MCPServerPattern(serverName string) string {
	return "mcp_" + sanitizeIdentifierComponent(serverName) + "_*"
}

// Name returns the tool name, prefixed with the server name.
// The total length is capped at 64 characters (OpenAI-compatible API limit).
// A short hash of the original (unsanitized) server and tool names is appended
// whenever sanitization is lossy or the name is truncated, ensuring that two
// names which differ only in disallowed characters remain distinct after sanitization.
func (t *MCPTool) Name() string {
	// Prefix with server name to avoid conflicts, and sanitize components
	sanitizedServer := sanitizeIdentifierComponent(t.serverName)
	sanitizedTool := sanitizeIdentifierComponent(t.tool.Name)
	full := fmt.Sprintf("mcp_%s_%s", sanitizedServer, sanitizedTool)

	// Check if sanitization was lossless (only lowercasing, no char replacement/truncation)
	lossless := strings.ToLower(t.serverName) == sanitizedServer &&
		strings.ToLower(t.tool.Name) == sanitizedTool

	const maxTotal = 64
	if lossless && len(full) <= maxTotal {
		return full
	}

	// Sanitization was lossy or name too long: append hash of the ORIGINAL names
	// (not the sanitized names) so different originals always yield different hashes.
	h := fnv.New32a()
	_, _ = h.Write([]byte(t.serverName + "\x00" + t.tool.Name))
	suffix := fmt.Sprintf("%08x", h.Sum32()) // 8 chars

	base := full
	if len(base) > maxTotal-9 {
		base = strings.TrimRight(full[:maxTotal-9], "_")
	}
	return base + "_" + suffix
}

// ExternalName is the name claw's MCP host advertises this upstream tool under to
// external clients: "<server>_<tool>" — claw's internal "mcp_" namespacing is
// dropped so clients see e.g. "fusion_trello_search" instead of the doubly-
// prefixed "mcp__claw__mcp_fusion_trello_search". The internal registry name
// (Name) is unchanged; only the externally published name differs. Implements
// the tools.ExternalNamer interface.
func (t *MCPTool) ExternalName() string {
	return strings.TrimPrefix(t.Name(), "mcp_")
}

// Description returns the tool description
func (t *MCPTool) Description() string {
	desc := t.tool.Description
	if desc == "" {
		desc = fmt.Sprintf("MCP tool from %s server", t.serverName)
	}
	// Add server info to description
	return fmt.Sprintf("[MCP:%s] %s", t.serverName, desc)
}

// Parameters returns the tool parameters schema as a plain JSON Schema object.
// A verbatim RawInputSchema (if the upstream provided one) wins; otherwise the
// structured InputSchema is marshaled to a map. An empty schema falls back to
// the object-with-no-properties shape downstream providers expect.
func (t *MCPTool) Parameters() map[string]any {
	if len(t.tool.RawInputSchema) > 0 {
		var result map[string]any
		if err := json.Unmarshal(t.tool.RawInputSchema, &result); err == nil {
			return result
		}
		return emptyObjectSchema()
	}

	// An empty Type means the server advertised no input schema.
	if t.tool.InputSchema.Type == "" {
		return emptyObjectSchema()
	}

	data, err := json.Marshal(t.tool.InputSchema)
	if err != nil {
		return emptyObjectSchema()
	}
	var result map[string]any
	if err := json.Unmarshal(data, &result); err != nil {
		return emptyObjectSchema()
	}
	return result
}

// emptyObjectSchema is the permissive no-argument schema used when an upstream
// tool advertises no usable input schema.
func emptyObjectSchema() map[string]any {
	return map[string]any{
		"type":       "object",
		"properties": map[string]any{},
		"required":   []string{},
	}
}

// Execute executes the MCP tool
func (t *MCPTool) Execute(ctx context.Context, args map[string]any) *ToolResult {
	result, err := t.manager.CallTool(ctx, t.serverName, t.tool.Name, args)
	if err != nil {
		return ErrorResult(fmt.Sprintf("MCP tool execution failed: %v", err)).WithError(err)
	}

	if result == nil {
		nilErr := fmt.Errorf("MCP tool returned nil result without error")
		return ErrorResult("MCP tool execution failed: nil result").WithError(nilErr)
	}

	// Handle error result from server
	if result.IsError {
		errMsg, _ := extractContent(result.Content)
		return ErrorResult(fmt.Sprintf("MCP tool returned error: %s", errMsg)).
			WithError(fmt.Errorf("MCP tool error: %s", errMsg))
	}

	// Extract text + any image content. Images are carried on Images (data URIs)
	// for a vision model to see; the text keeps an "[Image: …]" marker so a
	// non-vision model still knows an image was returned.
	output, images := extractContent(result.Content)

	return &ToolResult{
		ForLLM:  output,
		Images:  images,
		IsError: false,
	}
}

// extractContent splits an MCP content array into the text sent to the model and
// any images (as "data:<mime>;base64,…" URIs).
func extractContent(content []mcp.Content) (string, []string) {
	var parts []string
	var images []string
	for _, c := range content {
		switch v := c.(type) {
		case mcp.TextContent:
			parts = append(parts, v.Text)
		case mcp.ImageContent:
			// mark3labs ImageContent.Data is already base64-encoded.
			if v.Data != "" && v.MIMEType != "" {
				images = append(images, fmt.Sprintf("data:%s;base64,%s", v.MIMEType, v.Data))
			}
			parts = append(parts, fmt.Sprintf("[Image: %s]", v.MIMEType))
		default:
			// For other content types, use string representation
			parts = append(parts, fmt.Sprintf("[Content: %T]", v))
		}
	}
	return strings.Join(parts, "\n"), images
}
