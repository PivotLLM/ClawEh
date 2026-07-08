package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
)

// MockMCPManager is a mock implementation of MCPManager interface for testing
type MockMCPManager struct {
	callToolFunc func(ctx context.Context, serverName, toolName string, arguments map[string]any) (*mcp.CallToolResult, error)
}

func (m *MockMCPManager) CallTool(
	ctx context.Context,
	serverName, toolName string,
	arguments map[string]any,
) (*mcp.CallToolResult, error) {
	if m.callToolFunc != nil {
		return m.callToolFunc(ctx, serverName, toolName, arguments)
	}
	return &mcp.CallToolResult{
		Content: []mcp.Content{
			mcp.TextContent{Text: "mock result"},
		},
		IsError: false,
	}, nil
}

// TestNewMCPTool verifies MCP tool creation
func TestNewMCPTool(t *testing.T) {
	manager := &MockMCPManager{}
	tool := mcp.Tool{
		Name:        "test_tool",
		Description: "A test tool",
		InputSchema: mcp.ToolInputSchema{
			Type: "object",
			Properties: map[string]any{
				"input": map[string]any{
					"type":        "string",
					"description": "Test input",
				},
			},
		},
	}

	mcpTool := NewMCPTool(manager, "test_server", tool)

	if mcpTool == nil {
		t.Fatal("NewMCPTool should not return nil")
	}
}

// TestMCPTool_Name verifies name sanitization and prefixing.
func TestMCPTool_Name(t *testing.T) {
	tests := []struct {
		name       string
		serverName string
		toolName   string
		expectName string
	}{
		{
			name:       "simple names",
			serverName: "filesystem",
			toolName:   "read_file",
			expectName: "mcp_filesystem_read_file",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			manager := &MockMCPManager{}
			tool := mcp.Tool{Name: tt.toolName}
			mcpTool := NewMCPTool(manager, tt.serverName, tool)
			if got := mcpTool.Name(); got != tt.expectName {
				t.Errorf("Name() = %q, want %q", got, tt.expectName)
			}
		})
	}
}

// TestMCPTool_Description verifies description formatting.
func TestMCPTool_Description(t *testing.T) {
	tests := []struct {
		name            string
		serverName      string
		toolDescription string
		expectContains  []string
	}{
		{
			name:            "with description",
			serverName:      "github",
			toolDescription: "Search repositories",
			expectContains:  []string{"[MCP:github]", "Search repositories"},
		},
		{
			name:            "empty description",
			serverName:      "filesystem",
			toolDescription: "",
			expectContains:  []string{"[MCP:filesystem]", "MCP tool from filesystem server"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			manager := &MockMCPManager{}
			tool := mcp.Tool{
				Name:        "test_tool",
				Description: tt.toolDescription,
			}
			mcpTool := NewMCPTool(manager, tt.serverName, tool)

			result := mcpTool.Description()

			for _, expected := range tt.expectContains {
				if !strings.Contains(result, expected) {
					t.Errorf("Description should contain '%s', got: %s", expected, result)
				}
			}
		})
	}
}

// TestMCPTool_Parameters verifies parameter schema conversion
func TestMCPTool_Parameters(t *testing.T) {
	tests := []struct {
		name           string
		tool           mcp.Tool
		expectType     string
		checkProperty  string
		expectProperty bool
	}{
		{
			name: "structured schema",
			tool: mcp.Tool{
				Name: "test_tool",
				InputSchema: mcp.ToolInputSchema{
					Type: "object",
					Properties: map[string]any{
						"query": map[string]any{
							"type":        "string",
							"description": "Search query",
						},
					},
					Required: []string{"query"},
				},
			},
			expectType:     "object",
			checkProperty:  "query",
			expectProperty: true,
		},
		{
			name:           "empty schema",
			tool:           mcp.Tool{Name: "test_tool"},
			expectType:     "object",
			expectProperty: false,
		},
		{
			name: "raw schema",
			tool: mcp.Tool{
				Name: "test_tool",
				RawInputSchema: json.RawMessage(`{
					"type": "object",
					"properties": {
						"repo": {
							"type": "string",
							"description": "Repository name"
						},
						"stars": {
							"type": "integer",
							"description": "Minimum stars"
						}
					},
					"required": ["repo"]
				}`),
			},
			expectType:     "object",
			checkProperty:  "repo",
			expectProperty: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			manager := &MockMCPManager{}
			mcpTool := NewMCPTool(manager, "test_server", tt.tool)

			params := mcpTool.Parameters()

			if params == nil {
				t.Fatal("Parameters should not be nil")
			}

			if params["type"] != tt.expectType {
				t.Errorf("Expected type '%s', got '%v'", tt.expectType, params["type"])
			}

			// Check if property exists when expected
			if tt.checkProperty != "" {
				properties, ok := params["properties"].(map[string]any)
				if !ok && tt.expectProperty {
					t.Errorf("Expected properties to be a map")
					return
				}
				if ok {
					_, hasProperty := properties[tt.checkProperty]
					if hasProperty != tt.expectProperty {
						t.Errorf("Expected property '%s' existence: %v, got: %v",
							tt.checkProperty, tt.expectProperty, hasProperty)
					}
				}
			}
		})
	}
}

// TestMCPTool_Execute_Success tests successful tool execution
func TestMCPTool_Execute_Success(t *testing.T) {
	manager := &MockMCPManager{
		callToolFunc: func(ctx context.Context, serverName, toolName string, arguments map[string]any) (*mcp.CallToolResult, error) {
			// Verify correct parameters passed
			if serverName != "github" {
				t.Errorf("Expected serverName 'github', got '%s'", serverName)
			}
			if toolName != "search_repos" {
				t.Errorf("Expected toolName 'search_repos', got '%s'", toolName)
			}

			return &mcp.CallToolResult{
				Content: []mcp.Content{
					mcp.TextContent{Text: "Found 3 repositories"},
				},
				IsError: false,
			}, nil
		},
	}

	tool := mcp.Tool{
		Name:        "search_repos",
		Description: "Search GitHub repositories",
	}
	mcpTool := NewMCPTool(manager, "github", tool)

	ctx := context.Background()
	args := map[string]any{
		"query": "golang mcp",
	}

	result := mcpTool.Execute(ctx, args)

	if result == nil {
		t.Fatal("Result should not be nil")
	}
	if result.IsError {
		t.Errorf("Expected no error, got error: %s", result.ForLLM)
	}
	if result.ForLLM != "Found 3 repositories" {
		t.Errorf("Expected 'Found 3 repositories', got '%s'", result.ForLLM)
	}
}

// TestMCPTool_Execute_ManagerError tests execution when manager returns error
func TestMCPTool_Execute_ManagerError(t *testing.T) {
	manager := &MockMCPManager{
		callToolFunc: func(ctx context.Context, serverName, toolName string, arguments map[string]any) (*mcp.CallToolResult, error) {
			return nil, fmt.Errorf("connection failed")
		},
	}

	tool := mcp.Tool{Name: "test_tool"}
	mcpTool := NewMCPTool(manager, "test_server", tool)

	ctx := context.Background()
	result := mcpTool.Execute(ctx, map[string]any{})

	if result == nil {
		t.Fatal("Result should not be nil")
	}
	if !result.IsError {
		t.Error("Expected IsError to be true")
	}
	if !strings.Contains(result.ForLLM, "MCP tool execution failed") {
		t.Errorf("Error message should mention execution failure, got: %s", result.ForLLM)
	}
	if !strings.Contains(result.ForLLM, "connection failed") {
		t.Errorf("Error message should include original error, got: %s", result.ForLLM)
	}
}

// TestMCPTool_Execute_ServerError tests execution when server returns error
func TestMCPTool_Execute_ServerError(t *testing.T) {
	manager := &MockMCPManager{
		callToolFunc: func(ctx context.Context, serverName, toolName string, arguments map[string]any) (*mcp.CallToolResult, error) {
			return &mcp.CallToolResult{
				Content: []mcp.Content{
					mcp.TextContent{Text: "Invalid API key"},
				},
				IsError: true,
			}, nil
		},
	}

	tool := mcp.Tool{Name: "test_tool"}
	mcpTool := NewMCPTool(manager, "test_server", tool)

	ctx := context.Background()
	result := mcpTool.Execute(ctx, map[string]any{})

	if result == nil {
		t.Fatal("Result should not be nil")
	}
	if !result.IsError {
		t.Error("Expected IsError to be true")
	}
	if !strings.Contains(result.ForLLM, "MCP tool returned error") {
		t.Errorf("Error message should mention server error, got: %s", result.ForLLM)
	}
	if !strings.Contains(result.ForLLM, "Invalid API key") {
		t.Errorf("Error message should include server message, got: %s", result.ForLLM)
	}
}

// TestMCPTool_Execute_MultipleContent tests execution with multiple content items
func TestMCPTool_Execute_MultipleContent(t *testing.T) {
	manager := &MockMCPManager{
		callToolFunc: func(ctx context.Context, serverName, toolName string, arguments map[string]any) (*mcp.CallToolResult, error) {
			return &mcp.CallToolResult{
				Content: []mcp.Content{
					mcp.TextContent{Text: "First line"},
					mcp.TextContent{Text: "Second line"},
					mcp.TextContent{Text: "Third line"},
				},
				IsError: false,
			}, nil
		},
	}

	tool := mcp.Tool{Name: "multi_output"}
	mcpTool := NewMCPTool(manager, "test_server", tool)

	ctx := context.Background()
	result := mcpTool.Execute(ctx, map[string]any{})

	if result.IsError {
		t.Errorf("Expected no error, got: %s", result.ForLLM)
	}

	expected := "First line\nSecond line\nThird line"
	if result.ForLLM != expected {
		t.Errorf("Expected '%s', got '%s'", expected, result.ForLLM)
	}
}

// TestExtractContentText_TextContent tests text content extraction
func TestExtractContentText_TextContent(t *testing.T) {
	content := []mcp.Content{
		mcp.TextContent{Text: "Hello World"},
		mcp.TextContent{Text: "Second message"},
	}

	result, _ := extractContent(content)
	expected := "Hello World\nSecond message"

	if result != expected {
		t.Errorf("Expected '%s', got '%s'", expected, result)
	}
}

// TestExtractContentText_ImageContent tests image content extraction
func TestExtractContentText_ImageContent(t *testing.T) {
	content := []mcp.Content{
		mcp.ImageContent{
			Data:     "base64data",
			MIMEType: "image/png",
		},
	}

	result, images := extractContent(content)

	if !strings.Contains(result, "[Image:") {
		t.Errorf("Expected image indicator, got: %s", result)
	}
	if !strings.Contains(result, "image/png") {
		t.Errorf("Expected MIME type in output, got: %s", result)
	}
	// The image must also be captured as a base64 data URI for vision passthrough.
	// mark3labs ImageContent.Data is already base64, so it is used verbatim.
	if len(images) != 1 {
		t.Fatalf("expected 1 captured image, got %d", len(images))
	}
	want := "data:image/png;base64,base64data"
	if images[0] != want {
		t.Errorf("image data URI = %q, want %q", images[0], want)
	}
}

// TestExtractContentText_MixedContent tests mixed content types
func TestExtractContentText_MixedContent(t *testing.T) {
	content := []mcp.Content{
		mcp.TextContent{Text: "Description"},
		mcp.ImageContent{
			Data:     "data",
			MIMEType: "image/jpeg",
		},
		mcp.TextContent{Text: "More text"},
	}

	result, _ := extractContent(content)

	if !strings.Contains(result, "Description") {
		t.Errorf("Should contain text content, got: %s", result)
	}
	if !strings.Contains(result, "[Image:") {
		t.Errorf("Should contain image indicator, got: %s", result)
	}
	if !strings.Contains(result, "More text") {
		t.Errorf("Should contain second text, got: %s", result)
	}
}

// TestExtractContentText_EmptyContent tests empty content array
func TestExtractContentText_EmptyContent(t *testing.T) {
	content := []mcp.Content{}

	result, _ := extractContent(content)

	if result != "" {
		t.Errorf("Expected empty string for empty content, got: %s", result)
	}
}

// TestMCPTool_InterfaceCompliance verifies MCPTool implements Tool interface
func TestMCPTool_InterfaceCompliance(t *testing.T) {
	manager := &MockMCPManager{}
	tool := mcp.Tool{Name: "test"}
	mcpTool := NewMCPTool(manager, "test_server", tool)

	// Verify it implements Tool interface
	var _ Tool = mcpTool
}

// TestMCPTool_Parameters_StructuredSchema tests a structured InputSchema round-trip
func TestMCPTool_Parameters_StructuredSchema(t *testing.T) {
	manager := &MockMCPManager{}
	tool := mcp.Tool{
		Name: "test_tool",
		InputSchema: mcp.ToolInputSchema{
			Type: "object",
			Properties: map[string]any{
				"name": map[string]any{
					"type":        "string",
					"description": "The name parameter",
				},
			},
			Required: []string{"name"},
		},
	}
	mcpTool := NewMCPTool(manager, "test_server", tool)

	params := mcpTool.Parameters()

	if params["type"] != "object" {
		t.Errorf("Expected type 'object', got '%v'", params["type"])
	}

	props, ok := params["properties"].(map[string]any)
	if !ok {
		t.Error("Properties should be a map")
	}

	nameParam, ok := props["name"].(map[string]any)
	if !ok {
		t.Error("Name parameter should exist")
	}

	if nameParam["type"] != "string" {
		t.Errorf("Name type should be 'string', got '%v'", nameParam["type"])
	}
}
