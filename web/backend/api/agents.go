package api

import (
	"net/http"
	"sort"

	"github.com/PivotLLM/ClawEh/config"
	"github.com/PivotLLM/ClawEh/tools"
	"github.com/PivotLLM/ClawEh/tools/fusion"
)

type agentToolEntry struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	// Suite, when set, marks an all-or-nothing suite (cogmem, maestro) managed by
	// the agent's per-suite toggle rather than this per-tool list.
	Suite string `json:"suite,omitempty"`
}

type agentMCPServer struct {
	Name    string `json:"name"`
	Pattern string `json:"pattern"`
}

type agentToolCatalogResponse struct {
	Tools      []agentToolEntry `json:"tools"`
	MCPServers []agentMCPServer `json:"mcp_servers,omitempty"`
	// FusionServices are the Fusion services defined in the fusion config
	// folder; an mcp_tools entry naming one grants the agent that service's
	// tools when its Fusion switch is on.
	FusionServices []string `json:"fusion_services,omitempty"`
	DefaultTools   []string `json:"default_tools"`
}

func (h *Handler) registerAgentRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/agents/tools", h.handleListAgentTools)
	mux.HandleFunc("GET /api/agents/human", h.handleHumanAgentProblems)
	mux.HandleFunc("GET /api/agents/mounts/ignored", h.handleIgnoredMounts)
}

// ignoredMount is a mount of the saved configuration that is not used because
// its name is reserved (config.IgnoredMounts); the Agents page marks it.
type ignoredMount struct {
	Agent string `json:"agent"`
	Mount string `json:"mount"`
}

// handleIgnoredMounts lists the mounts set aside for a reserved name.
func (h *Handler) handleIgnoredMounts(w http.ResponseWriter, _ *http.Request) {
	cfg, err := h.currentConfig()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	out := []ignoredMount{}
	for _, m := range cfg.IgnoredMounts() {
		out = append(out, ignoredMount{Agent: m.Agent, Mount: m.Mount})
	}
	w.Header().Set("Content-Type", "application/json")
	encodeJSON(w, map[string]any{"ignored": out})
}

// humanAgentProblem is one way the configuration breaks the human-agent
// rules, placed where the WebUI shows it:
//   - "not_running": the agent named is not run (shown on its card);
//   - "ignored": a human model the agent names is ignored there (a note on
//     its card);
//   - "setting": a global setting ignores a human model (shown on Page (/system, /models), the
//     page that sets it).
//
// Link, when set, is the page that fixes it ("/channels" for a missing chat).
type humanAgentProblem struct {
	Agent   string `json:"agent,omitempty"`
	Kind    string `json:"kind"`
	Message string `json:"message"`
	Link    string `json:"link,omitempty"`
	Page    string `json:"page,omitempty"`
}

// handleHumanAgentProblems lists the human agents and the human-agent
// problems of the saved configuration (config.HumanProblems).
func (h *Handler) handleHumanAgentProblems(w http.ResponseWriter, _ *http.Request) {
	cfg, err := h.currentConfig()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	problems := []humanAgentProblem{}
	for _, p := range cfg.HumanProblems() {
		hp := humanAgentProblem{Agent: p.Agent, Message: p.Message}
		switch {
		case p.SetsAgentAside():
			hp.Kind = "not_running"
		case p.Agent != "":
			hp.Kind = "ignored"
		case p.Kind == config.HumanSharedName:
			hp.Kind, hp.Page = "setting", "/models"
		default:
			hp.Kind, hp.Page = "setting", "/system"
		}
		if p.Kind == config.HumanNoChat {
			hp.Link = "/channels"
		}
		problems = append(problems, hp)
	}
	humans := []string{}
	for i := range cfg.Agents.List {
		if cfg.IsHumanAgent(cfg.Agents.List[i].ID) {
			humans = append(humans, cfg.Agents.List[i].ID)
		}
	}
	w.Header().Set("Content-Type", "application/json")
	encodeJSON(w, map[string]any{"problems": problems, "human_agents": humans})
}

func (h *Handler) handleListAgentTools(w http.ResponseWriter, r *http.Request) {
	// Flat list of built-in tools in display order, built dynamically from providers.
	var builtinTools []agentToolEntry
	for _, p := range tools.GetProviders() {
		for _, d := range p.Describe() {
			builtinTools = append(builtinTools, agentToolEntry{
				Name:        d.Name,
				Description: d.Description,
				Suite:       d.Suite,
			})
		}
	}
	// Append static (non-provider) tools.
	for _, d := range staticToolDescriptors {
		builtinTools = append(builtinTools, agentToolEntry{
			Name:        d.Name,
			Description: d.Description,
		})
	}

	// MCP servers: one entry per configured server; selecting it grants mcp_name_* access
	var mcpServers []agentMCPServer
	if cfg, err := h.currentConfig(); err == nil && cfg.Tools.MCPClientEffectivelyEnabled() {
		for name := range cfg.Tools.MCP.Servers {
			mcpServers = append(mcpServers, agentMCPServer{
				Name:    name,
				Pattern: tools.MCPServerPattern(name),
			})
		}
		sort.Slice(mcpServers, func(i, j int) bool {
			return mcpServers[i].Name < mcpServers[j].Name
		})
	}

	effectiveDefaults := config.DefaultAgentTools
	var fusionServices []string
	if cfg, cfgErr := h.currentConfig(); cfgErr == nil {
		if len(cfg.Agents.Defaults.DefaultTools) > 0 {
			effectiveDefaults = cfg.Agents.Defaults.DefaultTools
		}
		fusionServices = fusion.ServiceNames(cfg) //nolint:contextcheck // the engine is process-wide and built once, outside any request
	}

	w.Header().Set("Content-Type", "application/json")
	encodeJSON(w, agentToolCatalogResponse{
		Tools:          builtinTools,
		MCPServers:     mcpServers,
		FusionServices: fusionServices,
		DefaultTools:   effectiveDefaults,
	})
}
