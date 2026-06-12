package api

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/PivotLLM/ClawEh/pkg/config"
)

// registerConfigRoutes binds configuration management endpoints to the ServeMux.
func (h *Handler) registerConfigRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/config", h.handleGetConfig)
	mux.HandleFunc("PUT /api/config", h.handleUpdateConfig)
	mux.HandleFunc("PATCH /api/config", h.handlePatchConfig)
}

// handleGetConfig returns the complete system configuration.
//
//	GET /api/config
func (h *Handler) handleGetConfig(w http.ResponseWriter, r *http.Request) {
	cfg, err := config.LoadConfig(h.configPath)
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to load config: %v", err), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(cfg); err != nil {
		http.Error(w, "Failed to encode response", http.StatusInternalServerError)
	}
}

// handleUpdateConfig updates the complete system configuration.
//
//	PUT /api/config
func (h *Handler) handleUpdateConfig(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		http.Error(w, "Failed to read request body", http.StatusBadRequest)
		return
	}
	defer r.Body.Close()

	var cfg config.Config
	if err := json.Unmarshal(body, &cfg); err != nil {
		http.Error(w, fmt.Sprintf("Invalid JSON: %v", err), http.StatusBadRequest)
		return
	}
	if execAllowRemoteOmitted(body) {
		cfg.Tools.Exec.AllowRemote = config.DefaultConfig().Tools.Exec.AllowRemote
	}

	if errs := validateConfig(&cfg); len(errs) > 0 {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]any{
			"status": "validation_error",
			"errors": errs,
		})
		return
	}

	if err := config.SaveConfig(h.configPath, &cfg); err != nil {
		http.Error(w, fmt.Sprintf("Failed to save config: %v", err), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

func execAllowRemoteOmitted(body []byte) bool {
	var raw struct {
		Tools *struct {
			Exec *struct {
				AllowRemote *bool `json:"allow_remote"`
			} `json:"exec"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return false
	}
	return raw.Tools == nil || raw.Tools.Exec == nil || raw.Tools.Exec.AllowRemote == nil
}

// handlePatchConfig partially updates the system configuration using JSON Merge Patch (RFC 7396).
// Only the fields present in the request body will be updated; all other fields remain unchanged.
//
//	PATCH /api/config
func (h *Handler) handlePatchConfig(w http.ResponseWriter, r *http.Request) {
	patchBody, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		http.Error(w, "Failed to read request body", http.StatusBadRequest)
		return
	}
	defer r.Body.Close()

	// Validate the patch is valid JSON
	var patch map[string]any
	if err = json.Unmarshal(patchBody, &patch); err != nil {
		http.Error(w, fmt.Sprintf("Invalid JSON: %v", err), http.StatusBadRequest)
		return
	}

	// Load existing config and marshal to a map for merging
	cfg, err := config.LoadConfig(h.configPath)
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to load config: %v", err), http.StatusInternalServerError)
		return
	}

	existing, err := json.Marshal(cfg)
	if err != nil {
		http.Error(w, "Failed to serialize current config", http.StatusInternalServerError)
		return
	}

	var base map[string]any
	if err = json.Unmarshal(existing, &base); err != nil {
		http.Error(w, "Failed to parse current config", http.StatusInternalServerError)
		return
	}

	// Recursively merge patch into base
	mergeMap(base, patch)

	// Convert merged map back to Config struct
	merged, err := json.Marshal(base)
	if err != nil {
		http.Error(w, "Failed to serialize merged config", http.StatusInternalServerError)
		return
	}

	var newCfg config.Config
	if err := json.Unmarshal(merged, &newCfg); err != nil {
		http.Error(w, fmt.Sprintf("Merged config is invalid: %v", err), http.StatusBadRequest)
		return
	}

	if errs := validateConfig(&newCfg); len(errs) > 0 {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]any{
			"status": "validation_error",
			"errors": errs,
		})
		return
	}

	if err := config.SaveConfig(h.configPath, &newCfg); err != nil {
		http.Error(w, fmt.Sprintf("Failed to save config: %v", err), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

// validateConfig checks the config for common errors before saving.
// Returns a list of human-readable error strings; empty means valid.
func validateConfig(cfg *config.Config) []string {
	var errs []string

	// At least one named agent must be configured
	if len(cfg.Agents.List) == 0 {
		errs = append(errs, "agents.list: at least one named agent must be configured")
	}

	// Validate models entries
	if err := cfg.ValidateModels(); err != nil {
		errs = append(errs, err.Error())
	}

	// Gateway port range
	if cfg.Gateway.Port != 0 && (cfg.Gateway.Port < 1 || cfg.Gateway.Port > 65535) {
		errs = append(errs, fmt.Sprintf("gateway.port %d is out of valid range (1-65535)", cfg.Gateway.Port))
	}

	// WebUI channel: token required when enabled
	if cfg.Channels.WebUI.Enabled && cfg.Channels.WebUI.Token == "" {
		errs = append(errs, "channels.webui.token is required when webui channel is enabled")
	}

	// Telegram: token required for each enabled bot
	for _, bot := range cfg.Channels.Telegram {
		if bot.Enabled && bot.Token == "" {
			errs = append(errs, "channels.telegram["+bot.ID+"].token is required when telegram bot is enabled")
		}
	}

	// Discord: token required when enabled
	if cfg.Channels.Discord.Enabled && cfg.Channels.Discord.Token == "" {
		errs = append(errs, "channels.discord.token is required when discord channel is enabled")
	}

	// MCP host: listen and endpoint_path well-formed
	if listen := strings.TrimSpace(cfg.MCPHost.Listen); listen != "" {
		if err := validateListenAddr(listen); err != nil {
			errs = append(errs, "mcp_host.listen: "+err.Error())
		}
	}
	if ep := strings.TrimSpace(cfg.MCPHost.EndpointPath); ep != "" && !strings.HasPrefix(ep, "/") {
		errs = append(errs, "mcp_host.endpoint_path must start with '/'")
	}

	// Per-agent memory_dir validation. Catches: NUL bytes, relative paths,
	// memory_dir == agent's workspace or any ancestor of any workspace, and
	// two agents sharing the same memory_dir. Sharing would let one agent's
	// notes overwrite another's.
	errs = append(errs, validateAgentMemoryDirs(cfg)...)

	return errs
}

// validateAgentMemoryDirs enforces the memory_dir rules described in
// AgentConfig.MemoryDir. Returns one error string per violation.
func validateAgentMemoryDirs(cfg *config.Config) []string {
	var errs []string

	// Precompute resolved workspaces and memory_dirs for every configured agent,
	// keyed by agent ID for clearer error messages.
	type resolved struct {
		id        string
		workspace string
		memoryDir string // empty when not set
	}
	agents := make([]resolved, 0, len(cfg.Agents.List))

	// Mirror resolveAgentWorkspace semantics from pkg/agent/instance.go.
	defaultWS := cfg.WorkspacePath()
	agentsDir := filepath.Dir(defaultWS)

	for _, ac := range cfg.Agents.List {
		ws := strings.TrimSpace(ac.Workspace)
		if ws != "" {
			ws = expandHomeForValidation(ws)
		} else if id := strings.ToLower(strings.TrimSpace(ac.ID)); id == "" || id == "main" {
			ws = defaultWS
		} else {
			ws = filepath.Join(agentsDir, id)
		}

		md := strings.TrimSpace(ac.MemoryDir)
		if md != "" {
			if strings.ContainsRune(md, 0) {
				errs = append(errs, fmt.Sprintf(
					"agents.list[%s].memory_dir: must not contain NUL bytes", ac.ID))
				md = "" // skip further checks on a clearly invalid value
			} else {
				expanded := expandHomeForValidation(md)
				if !filepath.IsAbs(expanded) {
					errs = append(errs, fmt.Sprintf(
						"agents.list[%s].memory_dir %q: must be an absolute path", ac.ID, md))
					md = ""
				} else {
					md = filepath.Clean(expanded)
				}
			}
		}
		agents = append(agents, resolved{id: ac.ID, workspace: filepath.Clean(ws), memoryDir: md})
	}

	// Self check: memory_dir must not equal or be an ancestor of its own workspace.
	for _, a := range agents {
		if a.memoryDir == "" {
			continue
		}
		if a.memoryDir == a.workspace || isAncestorPath(a.memoryDir, a.workspace) {
			errs = append(errs, fmt.Sprintf(
				"agents.list[%s].memory_dir %q: must not equal nor be an ancestor of its own workspace %q",
				a.id, a.memoryDir, a.workspace))
		}
	}

	// Cross check: memory_dir must not equal nor be an ancestor of any other
	// agent's workspace or memory_dir. Sharing a memory_dir between two agents
	// is rejected outright.
	for i, a := range agents {
		if a.memoryDir == "" {
			continue
		}
		for j, b := range agents {
			if i == j {
				continue
			}
			if a.memoryDir == b.workspace || isAncestorPath(a.memoryDir, b.workspace) {
				errs = append(errs, fmt.Sprintf(
					"agents.list[%s].memory_dir %q: must not equal nor be an ancestor of agent %s workspace %q",
					a.id, a.memoryDir, b.id, b.workspace))
			}
			if b.memoryDir != "" {
				if a.memoryDir == b.memoryDir {
					// Only emit once per pair (i < j) to avoid duplicate messages.
					if i < j {
						errs = append(errs, fmt.Sprintf(
							"agents.list[%s].memory_dir and agents.list[%s].memory_dir both resolve to %q: two agents must not share a memory directory",
							a.id, b.id, a.memoryDir))
					}
				} else if isAncestorPath(a.memoryDir, b.memoryDir) {
					errs = append(errs, fmt.Sprintf(
						"agents.list[%s].memory_dir %q: must not be an ancestor of agent %s memory_dir %q",
						a.id, a.memoryDir, b.id, b.memoryDir))
				}
			}
		}
	}

	return errs
}

// expandHomeForValidation mirrors config.expandHome (which is unexported).
// Keeping the implementation local avoids exposing the helper just for the
// web layer.
func expandHomeForValidation(path string) string {
	if path == "" {
		return path
	}
	if path[0] != '~' {
		return path
	}
	home, _ := os.UserHomeDir()
	if len(path) > 1 && path[1] == '/' {
		return home + path[1:]
	}
	if len(path) == 1 {
		return home
	}
	return path
}

// isAncestorPath returns true when ancestor is a strict ancestor directory of
// descendant. Both inputs must be cleaned absolute paths.
func isAncestorPath(ancestor, descendant string) bool {
	if ancestor == "" || descendant == "" || ancestor == descendant {
		return false
	}
	rel, err := filepath.Rel(ancestor, descendant)
	if err != nil {
		return false
	}
	if rel == "." || rel == "" {
		return false
	}
	// Reject ".." entries — those indicate descendant is OUTSIDE ancestor.
	return filepath.IsLocal(rel)
}

// validateListenAddr checks that s is a host:port with port in 1-65535.
func validateListenAddr(s string) error {
	lastColon := strings.LastIndex(s, ":")
	if lastColon <= 0 || lastColon == len(s)-1 {
		return fmt.Errorf("must be host:port (e.g. 127.0.0.1:5911)")
	}
	portStr := s[lastColon+1:]
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return fmt.Errorf("port %q is not an integer", portStr)
	}
	if port < 1 || port > 65535 {
		return fmt.Errorf("port %d is out of valid range (1-65535)", port)
	}
	return nil
}

// mergeMap recursively merges src into dst (JSON Merge Patch semantics).
// - If a key in src has a null value, it is deleted from dst.
// - If both dst and src have a nested object for the same key, merge recursively.
// - Otherwise the value from src overwrites dst.
func mergeMap(dst, src map[string]any) {
	for key, srcVal := range src {
		if srcVal == nil {
			delete(dst, key)
			continue
		}
		srcMap, srcIsMap := srcVal.(map[string]any)
		dstMap, dstIsMap := dst[key].(map[string]any)
		if srcIsMap && dstIsMap {
			mergeMap(dstMap, srcMap)
		} else {
			dst[key] = srcVal
		}
	}
}
