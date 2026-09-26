package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/PivotLLM/ClawEh/config"
	"github.com/PivotLLM/ClawEh/internal/backup"
	"github.com/PivotLLM/ClawEh/logger"
	"github.com/PivotLLM/ClawEh/utils"
)

// registerConfigRoutes binds configuration management endpoints to the ServeMux.
func (h *Handler) registerConfigRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/config", h.handleGetConfig)
	mux.HandleFunc("PUT /api/config", h.handleUpdateConfig)
	mux.HandleFunc("PATCH /api/config", h.handlePatchConfig)
	mux.HandleFunc("POST /api/backup", h.handleRunBackup)
}

// handleRunBackup runs an on-demand backup archive into the configured backup
// destination, regardless of the nightly toggle. "folder" is the destination
// directory (what the WebUI shows), "archive" the tarball written into it.
//
//	POST /api/backup
func (h *Handler) handleRunBackup(w http.ResponseWriter, r *http.Request) {
	cfg, err := h.currentConfig()
	if err != nil {
		http.Error(w, "failed to load config", http.StatusInternalServerError)
		return
	}
	res, err := backup.RunForConfig(cfg, h.configPath, time.Now(), backup.Options{}) //nolint:contextcheck // RunForConfig takes no context; the integrity check runs its PRAGMA on a detached context, and a started backup must finish even if the request is dropped
	if err != nil {
		http.Error(w, "backup failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	skipped := make([]string, 0, len(res.Skipped))
	for _, s := range res.Skipped {
		skipped = append(skipped, s.Path)
	}
	w.Header().Set("Content-Type", "application/json")
	encodeJSON(w, map[string]any{
		"folder":  backup.DestFor(cfg, ""),
		"archive": res.Archive,
		"bytes":   res.Bytes,
		"files":   res.Files,
		"skipped": skipped,
	})
}

// handleGetConfig returns the complete system configuration.
//
//	GET /api/config
func (h *Handler) handleGetConfig(w http.ResponseWriter, r *http.Request) {
	cfg, err := h.currentConfig()
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to load config: %v", err), http.StatusInternalServerError)
		return
	}

	// Credentials are masked here: this endpoint has no operator auth, so the
	// response must not be a dump of every key the gateway holds. PUT and PATCH
	// restore masked values from the live config, so a read-edit-write round
	// trip is safe. A secret reference ("env:NAME") is shown as written.
	out, err := maskedConfigJSON(cfg)
	if err != nil {
		http.Error(w, "Failed to encode response", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if _, err := w.Write(out); err != nil {
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
	defer utils.CloseQuietly(r.Body)

	// A client that read the masked config and is writing it back sends "****"
	// in place of each credential; swap those for the stored values so the round
	// trip does not destroy them.
	current, cerr := h.currentConfig()
	if cerr == nil {
		if stored, serr := config.MarshalWithSecretRefs(current); serr == nil {
			if restored, rerr := restoreMaskedSecrets(body, stored); rerr == nil {
				body = restored
			}
		}
	}

	var cfg config.Config
	if err := json.Unmarshal(body, &cfg); err != nil {
		http.Error(w, fmt.Sprintf("Invalid JSON: %v", err), http.StatusBadRequest)
		return
	}
	if execAllowRemoteOmitted(body) {
		cfg.Tools.Exec.AllowRemote = config.DefaultConfig().Tools.Exec.AllowRemote
	}

	if !h.saveValidatedConfig(w, func(c *config.Config) { *c = cfg }) {
		return
	}
	recordConfigWrite(r, current, &cfg)

	w.Header().Set("Content-Type", "application/json")
	encodeJSON(w, map[string]string{"status": "ok"})
}

// saveValidatedConfig runs mutate on the live config under the store lock,
// rejects the result with a validation_error response when validateConfig
// finds fault, and saves it otherwise. It reports whether the save happened;
// on false a response has been written.
func (h *Handler) saveValidatedConfig(w http.ResponseWriter, mutate func(*config.Config)) bool {
	var errs []string
	err := h.updateConfig(func(c *config.Config) error {
		mutate(c)
		if errs = validateConfig(c); len(errs) > 0 {
			return errValidation
		}
		return nil
	})
	switch {
	case errors.Is(err, errValidation):
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		encodeJSON(w, map[string]any{
			"status": "validation_error",
			"errors": errs,
		})
		return false
	case err != nil:
		writeUpdateError(w, err)
		return false
	}
	return true
}

// errValidation marks an updateConfig callback that stopped on validateConfig
// findings, which the caller reports in the validation_error shape.
var errValidation = errors.New("config validation failed")

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
	defer utils.CloseQuietly(r.Body)

	// Validate the patch is valid JSON
	var patch map[string]any
	if err = json.Unmarshal(patchBody, &patch); err != nil {
		http.Error(w, fmt.Sprintf("Invalid JSON: %v", err), http.StatusBadRequest)
		return
	}

	// The live config, as the file holds it (references as references), is the
	// base the patch merges into.
	cfg, err := h.currentConfig()
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to load config: %v", err), http.StatusInternalServerError)
		return
	}

	existing, err := config.MarshalWithSecretRefs(cfg)
	if err != nil {
		http.Error(w, "Failed to serialize current config", http.StatusInternalServerError)
		return
	}

	var base map[string]any
	if err = json.Unmarshal(existing, &base); err != nil {
		http.Error(w, "Failed to parse current config", http.StatusInternalServerError)
		return
	}

	// Restore any credential the client echoed back masked, before merging.
	unmaskSecrets(patch, base)

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

	if !h.saveValidatedConfig(w, func(c *config.Config) { *c = newCfg }) {
		return
	}
	recordConfigWrite(r, cfg, &newCfg)

	w.Header().Set("Content-Type", "application/json")
	encodeJSON(w, map[string]string{"status": "ok"})
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

	// Every agent, default, summarization and subagent chain must name a model
	// that exists. A reference to a disabled model is allowed (disabling is a
	// legitimate temporary action) but has no channel back to the client, so it
	// is logged instead.
	refErrs, refWarnings := cfg.ValidateModelReferences()
	for _, err := range refErrs {
		errs = append(errs, err.Error())
	}
	for _, w := range refWarnings {
		logger.WarnCF("config", "reference to disabled model", map[string]any{"detail": w})
	}

	// Validate agent bindings (default-channel constraints)
	if err := cfg.ValidateBindings(); err != nil {
		errs = append(errs, err.Error())
	}

	// Gateway port range
	if cfg.Gateway.Port != 0 && (cfg.Gateway.Port < 1 || cfg.Gateway.Port > 65535) {
		errs = append(errs, fmt.Sprintf("gateway.port %d is out of valid range (1-65535)", cfg.Gateway.Port))
	}

	// Listener settings LoadConfig refuses (a half-configured certificate, an
	// off-box MCP host) must be refused here too, or a WebUI save could write a
	// config the gateway then cannot start on.
	if err := cfg.Gateway.Validate(); err != nil {
		errs = append(errs, err.Error())
	}
	if err := config.ValidateMCPHostListen(cfg.MCPHost.Listen); err != nil {
		errs = append(errs, err.Error())
	}

	// Gateway IP allowlist: every entry must be a valid CIDR.
	if err := config.ValidateAllowedCIDRs(cfg.Gateway.AllowedCIDRs); err != nil {
		errs = append(errs, "gateway.allowed_cidrs: "+err.Error())
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

	return errs
}

// validateListenAddr checks that s is a host:port with port in 1-65535.
func validateListenAddr(s string) error {
	lastColon := strings.LastIndex(s, ":")
	if lastColon <= 0 || lastColon == len(s)-1 {
		return errors.New("must be host:port (e.g. 127.0.0.1:5911)")
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
