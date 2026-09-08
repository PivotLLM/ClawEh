package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os/exec"
	"strings"
	"time"

	"github.com/PivotLLM/ClawEh/config"
)

// The CLI agents section.
//
// A CLI agent is one thing to the person using it — "Antigravity" — and three
// to the configuration: a provider, a model, and a set of flags that are
// mandatory, undiscoverable, and silent when wrong. Building it by hand meant
// visiting two pages and knowing four values that appear in no form; the flags
// in particular were not editable in the WebUI at all, so a CLI model created
// through the browser could not be made to work.
//
// This endpoint presents the catalogue instead: every supported CLI, whether
// its binary is installed, and one switch. Turning it on creates whatever is
// missing; turning it off disables every model that runs through it.

// cliInfo is one row of the CLI section.
type cliInfo struct {
	Protocol string `json:"protocol"`
	Label    string `json:"label"`
	Binary   string `json:"binary"`
	// Installed is whether the binary was found. Rows stay listed when it was
	// not, greyed out: a CLI ClawEh supports but the host lacks is a different
	// thing from one it does not support, and hiding it looks like the latter.
	Installed bool   `json:"installed"`
	Path      string `json:"path,omitempty"`
	Version   string `json:"version,omitempty"`
	// Enabled is whether any model reaching this CLI is enabled — what the
	// switch shows. There is no per-provider enabled flag in the config; a
	// model's `enabled` is what dispatch actually honours.
	Enabled bool `json:"enabled"`
	// Configured is whether a provider for this CLI exists at all.
	Configured bool `json:"configured"`
	// ProviderIndex addresses that provider, so the section can offer the
	// ordinary edit sheet for the things the switch does not cover — chiefly
	// pinning an explicit binary path. -1 when there is no provider yet.
	ProviderIndex int `json:"provider_index"`
	// Models and ModelsEnabled count the models running through this CLI, so a
	// switch governing several of them does not hide what it is about to do.
	Models        int `json:"models"`
	ModelsEnabled int `json:"models_enabled"`
	// BaseArgs are the arguments the provider always passes — headless mode,
	// JSON output, read from stdin. RequiredArgs are the permission flags.
	// Both are reported so the row can show the whole command line: an operator
	// asking what ClawEh runs on their machine is owed all of it, not the part
	// that happens to live in config.
	BaseArgs     []string `json:"base_args"`
	RequiredArgs []string `json:"required_args"`
	// ExtraArgs are what the CLI's models add on top, deduplicated across them.
	ExtraArgs []string `json:"extra_args,omitempty"`
	// TrailingArgs come last, after the model flag — the stdin marker.
	TrailingArgs []string `json:"trailing_args,omitempty"`
}

func (h *Handler) registerSystemCLIRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/system/clis", h.handleListCLIs)
	mux.HandleFunc("PUT /api/system/clis/{protocol}", h.handleSetCLIEnabled)
}

// handleListCLIs reports every supported CLI agent: whether its binary is
// installed, and how it is currently configured.
//
//	GET /api/system/clis
func (h *Handler) handleListCLIs(w http.ResponseWriter, r *http.Request) {
	cfg, err := config.LoadConfig(h.configPath)
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to load config: %v", err), http.StatusInternalServerError)
		return
	}

	out := make([]cliInfo, 0, len(config.CLIAgents))
	for _, c := range config.CLIAgents {
		info := cliInfo{
			Protocol:      c.Protocol,
			Label:         c.Label,
			Binary:        c.Binary,
			ProviderIndex: -1,
			BaseArgs:      c.BaseArgs,
			RequiredArgs:  c.RequiredArgs,
			TrailingArgs:  c.TrailingArgs,
		}
		if p, err := exec.LookPath(c.Binary); err == nil {
			info.Installed = true
			info.Path = p
			info.Version = cliVersion(c.Binary)
		}
		// Seeded with the required arguments: a model that lists a flag the
		// protocol already supplies is not adding anything, and reporting it
		// again printed "--yolo --yolo". The invocation itself was always
		// correct — config.CLIArgs deduplicates — so this was the display
		// disagreeing with the command line.
		seenExtra := make(map[string]struct{}, len(c.RequiredArgs))
		for _, a := range c.RequiredArgs {
			seenExtra[a] = struct{}{}
		}
		for _, m := range cliModels(cfg, c.Protocol) {
			info.Configured = true
			info.Models++
			if m.Enabled {
				info.ModelsEnabled++
				info.Enabled = true
			}
			for _, a := range m.ExtraArgs {
				if _, dup := seenExtra[a]; dup {
					continue
				}
				seenExtra[a] = struct{}{}
				info.ExtraArgs = append(info.ExtraArgs, a)
			}
		}
		if idx := cliProviderIndex(cfg, c.Protocol); idx >= 0 {
			info.Configured = true
			info.ProviderIndex = idx
		}
		out = append(out, info)
	}
	writeJSON(w, http.StatusOK, out)
}

// handleSetCLIEnabled turns a CLI agent on or off.
//
//	PUT /api/system/clis/{protocol}   {"enabled": true}
//
// On: creates the provider and a sentinel model if they are missing, and
// enables the sentinel. The sentinel model's id is the protocol name, which the
// providers recognise as "pass no --model flag and let the CLI choose" — the
// right default for someone who has just switched a CLI on and named nothing.
//
// Off: disables every model reaching this CLI, rather than only the sentinel.
// A switch labelled with the CLI's name has to mean the CLI, or turning it off
// would leave an agent still routing to it through a second model.
//
// Nothing is deleted either way, so the switch is reversible: turning it back
// on finds the entries it left behind.
func (h *Handler) handleSetCLIEnabled(w http.ResponseWriter, r *http.Request) {
	protocol := r.PathValue("protocol")
	agent := config.CLIAgentByProtocol(protocol)
	if agent == nil {
		http.Error(w, fmt.Sprintf("Unknown CLI protocol %q", protocol), http.StatusNotFound)
		return
	}

	var body struct {
		Enabled *bool `json:"enabled"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&body); err != nil {
		http.Error(w, fmt.Sprintf("Invalid JSON: %v", err), http.StatusBadRequest)
		return
	}
	if body.Enabled == nil {
		http.Error(w, "enabled is required", http.StatusBadRequest)
		return
	}

	cfg, err := config.LoadConfig(h.configPath)
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to load config: %v", err), http.StatusInternalServerError)
		return
	}

	if !*body.Enabled {
		disableCLIModels(cfg, agent.Protocol)
	} else if err := enableCLI(cfg, agent); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	if err := config.SaveConfig(h.configPath, cfg); err != nil {
		http.Error(w, fmt.Sprintf("Failed to save config: %v", err), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "enabled": *body.Enabled})
}

// enableCLI makes a CLI usable, creating whatever is missing.
//
// An existing sentinel model is enabled in place rather than replaced: it may
// have been tuned (a context window, a workspace), and a switch is not a reset.
// When several models already reach this CLI but none is a sentinel, they are
// all enabled — the user configured them deliberately, and adding a sentinel
// alongside would be a model they did not ask for.
func enableCLI(cfg *config.Config, agent *config.CLIAgent) error {
	prov := cliProviders(cfg, agent.Protocol)
	if prov == nil {
		name := uniqueProviderName(cfg, agent.Label)
		cfg.Providers = append(cfg.Providers, config.Provider{
			Name:     name,
			Protocol: agent.Protocol,
			// No Command: an empty one resolves the catalogue's binary on PATH,
			// which follows the CLI across upgrades instead of pinning a path
			// that a reinstall invalidates.
		})
		prov = &cfg.Providers[len(cfg.Providers)-1]
	}

	models := cliModels(cfg, agent.Protocol)
	if len(models) == 0 {
		cfg.Models = append(cfg.Models, config.ModelConfig{
			ModelName:      uniqueModelName(cfg, agent.Label),
			Model:          agent.SentinelModel(),
			Provider:       prov.Name,
			RequestTimeout: agent.RequestTimeout,
			Enabled:        true,
		})
		return nil
	}
	for _, m := range models {
		if m.Model == agent.SentinelModel() {
			m.Enabled = true
			return nil
		}
	}
	for _, m := range models {
		m.Enabled = true
	}
	return nil
}

// disableCLIModels disables every model reaching this CLI.
func disableCLIModels(cfg *config.Config, protocol string) {
	for _, m := range cliModels(cfg, protocol) {
		m.Enabled = false
	}
}

// cliProviders returns the first provider using this CLI protocol, treating the
// deprecated gemini-cli alias as antigravity-cli so an old config's provider is
// found rather than duplicated.
func cliProviders(cfg *config.Config, protocol string) *config.Provider {
	if i := cliProviderIndex(cfg, protocol); i >= 0 {
		return &cfg.Providers[i]
	}
	return nil
}

// cliProviderIndex returns the config index of the first provider speaking this
// CLI protocol, or -1. The index is what the WebUI's edit and delete routes
// address a provider by.
func cliProviderIndex(cfg *config.Config, protocol string) int {
	want := config.CLIAgentByProtocol(protocol)
	if want == nil {
		return -1
	}
	for i := range cfg.Providers {
		p := &cfg.Providers[i]
		if config.IsCLIProtocol(p.Protocol) && config.CLIAgentByProtocol(p.Protocol) == want {
			return i
		}
	}
	return -1
}

// cliModels returns every model reaching this CLI protocol, through any
// provider that speaks it.
func cliModels(cfg *config.Config, protocol string) []*config.ModelConfig {
	want := config.CLIAgentByProtocol(protocol)
	if want == nil {
		return nil
	}
	names := map[string]struct{}{}
	for i := range cfg.Providers {
		p := &cfg.Providers[i]
		if config.IsCLIProtocol(p.Protocol) && config.CLIAgentByProtocol(p.Protocol) == want {
			names[p.Name] = struct{}{}
		}
	}
	var out []*config.ModelConfig
	for i := range cfg.Models {
		if _, ok := names[cfg.Models[i].Provider]; ok {
			out = append(out, &cfg.Models[i])
		}
	}
	return out
}

// uniqueProviderName returns base, or base with a numeric suffix when taken.
// Provider names are the key models reference, so a collision would attach the
// new provider's models to somebody else's endpoint.
func uniqueProviderName(cfg *config.Config, base string) string {
	taken := make(map[string]struct{}, len(cfg.Providers))
	for i := range cfg.Providers {
		taken[cfg.Providers[i].Name] = struct{}{}
	}
	return uniqueName(base, taken)
}

func uniqueModelName(cfg *config.Config, base string) string {
	taken := make(map[string]struct{}, len(cfg.Models))
	for i := range cfg.Models {
		taken[cfg.Models[i].ModelName] = struct{}{}
	}
	return uniqueName(base, taken)
}

func uniqueName(base string, taken map[string]struct{}) string {
	if _, clash := taken[base]; !clash {
		return base
	}
	for n := 2; ; n++ {
		candidate := fmt.Sprintf("%s %d", base, n)
		if _, clash := taken[candidate]; !clash {
			return candidate
		}
	}
}

// cliVersion runs "<bin> --version" with a short timeout and returns the first
// line, best-effort (empty string on any error/timeout).
func cliVersion(bin string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, bin, "--version").Output()
	if err != nil {
		return ""
	}
	line := strings.TrimSpace(string(out))
	if i := strings.IndexByte(line, '\n'); i >= 0 {
		line = strings.TrimSpace(line[:i])
	}
	return line
}
