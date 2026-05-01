// ClawEh
// License: MIT

package mcpserver

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// claudeProjectConfig is the JSON structure for the .claude.json workspace file.
type claudeProjectConfig struct {
	MCPServers map[string]claudeMCPServer `json:"mcpServers"`
}

type claudeMCPServer struct {
	Type string `json:"type"`
	URL  string `json:"url"`
}

// WriteAgentWorkspaceConfig writes (or updates) the .claude.json file in the given
// workspace directory to point the "claw" MCP server at agentURL. Existing entries
// for other servers are preserved. This lets per-agent claude subprocesses use the
// correct per-agent MCP endpoint (workspace-scoped tools).
//
// The file is written atomically using os.WriteFile with mode 0644.
func WriteAgentWorkspaceConfig(workspace, agentURL string) error {
	if workspace == "" || agentURL == "" {
		return nil
	}

	configPath := filepath.Join(workspace, ".claude.json")

	var cfg claudeProjectConfig
	if data, err := os.ReadFile(configPath); err == nil {
		if jsonErr := json.Unmarshal(data, &cfg); jsonErr != nil {
			return fmt.Errorf("existing %s is not valid JSON: %w", configPath, jsonErr)
		}
	}
	if cfg.MCPServers == nil {
		cfg.MCPServers = make(map[string]claudeMCPServer)
	}

	// If already correct, skip writing.
	if existing, ok := cfg.MCPServers["claw"]; ok &&
		existing.Type == "http" && existing.URL == agentURL {
		return nil
	}

	cfg.MCPServers["claw"] = claudeMCPServer{Type: "http", URL: agentURL}

	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(configPath, data, 0o644)
}
