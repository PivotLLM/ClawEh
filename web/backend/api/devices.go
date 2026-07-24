package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/PivotLLM/ClawEh/pkg/channels/device"
	"github.com/PivotLLM/ClawEh/pkg/config"
	"github.com/PivotLLM/ClawEh/pkg/routing"
)

// registerDeviceRoutes wires the external-device gateway onboarding/management API.
//
// SECURITY: these endpoints expose the pairing QR (which embeds the shared gateway
// token) and the approve/reject controls. They inherit the same auth posture as the
// rest of /api/* — see web/backend/server.go. Keep them behind the operator auth the
// setup wizard adds; do not expose this API on an untrusted network.
func (h *Handler) registerDeviceRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/devices/pair", h.handleDevicePair)      // provision token + enable + render QR
	mux.HandleFunc("GET /api/devices/pair", h.handleDevicePairStatus) // read-only current pairing config
	mux.HandleFunc("POST /api/devices/settings", h.handleDeviceSettings)
	mux.HandleFunc("POST /api/devices/word-token/regenerate", h.handleDeviceWordTokenRegenerate)
	mux.HandleFunc("GET /api/devices/pending", h.handleDevicePending)
	mux.HandleFunc("POST /api/devices/pending/{id}/approve", h.handleDeviceApprove)
	mux.HandleFunc("POST /api/devices/pending/{id}/reject", h.handleDeviceReject)
	mux.HandleFunc("GET /api/devices", h.handleDeviceList)
	mux.HandleFunc("POST /api/devices/{id}/agent", h.handleDeviceAgent)
	mux.HandleFunc("DELETE /api/devices/{id}", h.handleDeviceRemove)
}

// openDeviceStore opens the gateway pairing DB at the configured data dir. The
// channel and this admin API share the same WAL database file.
func (h *Handler) openDeviceStore() (*device.Store, *config.Config, error) {
	cfg, err := config.LoadConfig(h.configPath)
	if err != nil {
		return nil, nil, err
	}
	stateDir := filepath.Join(cfg.DataDir(), "state")
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return nil, nil, err
	}
	store, err := device.OpenStore(filepath.Join(stateDir, "gateway.db"))
	if err != nil {
		return nil, nil, err
	}
	return store, cfg, nil
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// handleDevicePair provisions the device gateway (generates+persists a shared token
// and enables the channel if needed), reloads the gateway, and returns the device
// setup payload plus a rendered QR (PNG data-URL + ASCII).
func (h *Handler) handleDevicePair(w http.ResponseWriter, _ *http.Request) {
	cfg, changed, err := device.EnsureProvisioned(h.configPath)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "provision failed"})
		return
	}
	// Only reload when provisioning actually changed config (first-time token/enable).
	// Regenerating the QR for an already-provisioned gateway must NOT bounce the
	// listener — that caused intermittent failures and dropped the device.
	if changed {
		if reload := h.reloadFunc(); reload != nil {
			_ = reload()
		}
	}
	writeJSON(w, http.StatusOK, h.buildPairResponse(cfg, true))
}

// handleDeviceSettings updates the device listener network settings (LAN toggle,
// External URL, enabled), persists them, reloads the gateway (which re-binds the
// device listener), and returns the refreshed pairing status.
func (h *Handler) handleDeviceSettings(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ListenLAN   *bool   `json:"listen_lan"`
		ExternalURL *string `json:"external_url"`
		Enabled     *bool   `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid body"})
		return
	}
	cfg, err := config.LoadConfig(h.configPath)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "config load failed"})
		return
	}
	d := &cfg.Channels.Device
	changed := false
	if body.ListenLAN != nil {
		newHost := "127.0.0.1"
		if *body.ListenLAN {
			newHost = "0.0.0.0"
		}
		if d.Host != newHost {
			d.Host = newHost
			changed = true
		}
	}
	if body.ExternalURL != nil {
		if v := strings.TrimSpace(*body.ExternalURL); v != d.ExternalURL {
			d.ExternalURL = v
			changed = true
		}
	}
	if body.Enabled != nil && d.Enabled != *body.Enabled {
		d.Enabled = *body.Enabled
		changed = true
	}
	// Only persist + reload (which re-binds the device listener) when something
	// actually changed, so saving with no edits is a cheap no-op.
	if changed {
		if err := config.SaveConfig(h.configPath, cfg); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "config save failed"})
			return
		}
		if reload := h.reloadFunc(); reload != nil {
			_ = reload()
		}
	}
	writeJSON(w, http.StatusOK, h.buildPairResponse(cfg, false))
}

// handleDeviceWordTokenRegenerate mints a fresh word passphrase, persists it, reloads
// the gateway so the new value takes effect, and returns the refreshed pairing status.
// The long QR token is left untouched.
func (h *Handler) handleDeviceWordTokenRegenerate(w http.ResponseWriter, _ *http.Request) {
	cfg, err := config.LoadConfig(h.configPath)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "config load failed"})
		return
	}
	wtok, werr := device.GenerateWordToken()
	if werr != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "word token generation failed"})
		return
	}
	cfg.Channels.Device.WordToken = wtok
	if err := config.SaveConfig(h.configPath, cfg); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "config save failed"})
		return
	}
	if reload := h.reloadFunc(); reload != nil {
		_ = reload()
	}
	writeJSON(w, http.StatusOK, h.buildPairResponse(cfg, false))
}

// handleDevicePairStatus returns the current pairing config without mutating it.
func (h *Handler) handleDevicePairStatus(w http.ResponseWriter, _ *http.Request) {
	cfg, err := config.LoadConfig(h.configPath)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "config load failed"})
		return
	}
	writeJSON(w, http.StatusOK, h.buildPairResponse(cfg, false))
}

// buildPairResponse assembles the setup payload, optional rendered QR, the device
// listener status, and operator warnings from the active config. The advertised
// endpoint comes from channels.device.external_url (or the LAN IPs by default), and
// the listener status reflects channels.device host/port (NOT the WebUI port).
func (h *Handler) buildPairResponse(cfg *config.Config, render bool) map[string]any {
	dev := cfg.Channels.Device
	devicePort := dev.Port
	if devicePort == 0 {
		devicePort = device.DefaultDevicePort
	}
	host := dev.Host
	if host == "" {
		host = "127.0.0.1"
	}
	token := dev.Token

	payload, perr := device.BuildSetupPayload(dev.ExternalURL, device.LANIPv4s(), devicePort, token)
	encoded := ""
	if perr == nil {
		encoded, _ = payload.Encode()
	}

	warnings := []string{}
	if device.IsLoopbackHost(host) {
		warnings = append(warnings, "Device gateway listens on loopback only ("+host+"); turn on \"listen for local network connections\" so devices can reach it.")
	}
	if dev.ExternalURL == "" && len(payload.IPs) == 0 {
		warnings = append(warnings, "No routable LAN IPv4 address detected; set an External URL.")
	}
	if perr != nil {
		warnings = append(warnings, perr.Error())
	}

	out := map[string]any{
		"payload":      encoded, // raw JSON string the device decodes from the QR
		"ips":          payload.IPs,
		"port":         payload.Port,
		"protocol":     payload.Protocol,
		"enabled":      dev.Enabled,
		"hasToken":     token != "",
		"word_token":   dev.WordToken, // typeable passphrase for clients that enter a token by hand
		"listen_host":  host,
		"listen_port":  devicePort,
		"listen_lan":   !device.IsLoopbackHost(host),
		"external_url": dev.ExternalURL,
		"warnings":     warnings,
	}
	if render && token != "" && encoded != "" {
		if pngURL, err := device.RenderQRCodePNGDataURL(encoded); err == nil {
			out["qr_png"] = pngURL
		}
		if ascii, err := device.RenderQRCodeASCII(encoded); err == nil {
			out["qr_ascii"] = ascii
		}
	}
	return out
}

type pendingDeviceView struct {
	RequestID   string `json:"request_id"`
	DeviceID    string `json:"device_id"`
	DisplayName string `json:"display_name"`
	Platform    string `json:"platform"`
	ClientID    string `json:"client_id"`
	Role        string `json:"role"`
	RemoteIP    string `json:"remote_ip"`
	CreatedAtMs int64  `json:"created_at_ms"`
}

func (h *Handler) handleDevicePending(w http.ResponseWriter, r *http.Request) {
	store, _, err := h.openDeviceStore()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "store open failed"})
		return
	}
	defer func() { _ = store.Close() }()
	pending, err := store.ListPending(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "list failed"})
		return
	}
	views := make([]pendingDeviceView, 0, len(pending))
	for _, p := range pending {
		views = append(views, pendingDeviceView{
			RequestID: p.RequestID, DeviceID: p.DeviceID, DisplayName: p.DisplayName,
			Platform: p.Platform, ClientID: p.ClientID, Role: p.Role, RemoteIP: p.RemoteIP, CreatedAtMs: p.CreatedAtMs,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"pending": views})
}

func (h *Handler) handleDeviceApprove(w http.ResponseWriter, r *http.Request) {
	h.resolvePending(w, r, true)
}

func (h *Handler) handleDeviceReject(w http.ResponseWriter, r *http.Request) {
	h.resolvePending(w, r, false)
}

func (h *Handler) resolvePending(w http.ResponseWriter, r *http.Request, approve bool) {
	requestID := r.PathValue("id")
	store, _, err := h.openDeviceStore()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "store open failed"})
		return
	}
	defer func() { _ = store.Close() }()

	if approve {
		dev, _, aerr := store.Approve(r.Context(), requestID, nil, nil)
		if errors.Is(aerr, device.ErrPendingNotFound) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "pending request not found"})
			return
		}
		if aerr != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "approve failed"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"status": "approved", "device_id": dev.DeviceID})
		return
	}

	if rerr := store.Reject(r.Context(), requestID); errors.Is(rerr, device.ErrPendingNotFound) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "pending request not found"})
		return
	} else if rerr != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "reject failed"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "rejected"})
}

type pairedDeviceView struct {
	DeviceID     string   `json:"device_id"`
	DisplayName  string   `json:"display_name"`
	Platform     string   `json:"platform"`
	ClientMode   string   `json:"client_mode"`
	Roles        []string `json:"roles"`
	Scopes       []string `json:"scopes"`
	AgentID      string   `json:"agent_id"` // per-device assigned agent ("" = default)
	ApprovedAtMs int64    `json:"approved_at_ms"`
	LastSeenAtMs int64    `json:"last_seen_at_ms"`
}

// agentOption is a selectable agent for the per-device assistant dropdown.
type agentOption struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// configuredAgents returns the selectable agents (normalized id + display name).
func configuredAgents(cfg *config.Config) []agentOption {
	out := make([]agentOption, 0, len(cfg.Agents.List))
	for i := range cfg.Agents.List {
		ac := &cfg.Agents.List[i]
		id := routing.NormalizeAgentID(ac.ID)
		name := ac.Name
		if name == "" {
			name = id
		}
		out = append(out, agentOption{ID: id, Name: name})
	}
	return out
}

func (h *Handler) handleDeviceList(w http.ResponseWriter, r *http.Request) {
	store, cfg, err := h.openDeviceStore()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "store open failed"})
		return
	}
	defer func() { _ = store.Close() }()
	paired, err := store.ListPaired(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "list failed"})
		return
	}
	views := make([]pairedDeviceView, 0, len(paired))
	for _, d := range paired {
		views = append(views, pairedDeviceView{
			DeviceID: d.DeviceID, DisplayName: d.DisplayName, Platform: d.Platform,
			ClientMode: d.ClientMode, Roles: d.Roles, Scopes: d.Scopes, AgentID: d.AgentID,
			ApprovedAtMs: d.ApprovedAtMs, LastSeenAtMs: d.LastSeenAtMs,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"devices": views, "agents": configuredAgents(cfg)})
}

// handleDeviceAgent assigns (or clears) the agent a paired device routes to. Body:
// {"agent_id": "<id>"} — empty string routes the device to the gateway default. Takes
// effect on the device's next turn; no reload needed (read live from the store).
func (h *Handler) handleDeviceAgent(w http.ResponseWriter, r *http.Request) {
	deviceID := r.PathValue("id")
	var body struct {
		AgentID string `json:"agent_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid body"})
		return
	}
	store, _, err := h.openDeviceStore()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "store open failed"})
		return
	}
	defer func() { _ = store.Close() }()
	if err := store.SetDeviceAgent(r.Context(), deviceID, strings.TrimSpace(body.AgentID)); err != nil {
		if errors.Is(err, device.ErrPairedNotFound) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "device not found"})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "update failed"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (h *Handler) handleDeviceRemove(w http.ResponseWriter, r *http.Request) {
	deviceID := r.PathValue("id")
	store, _, err := h.openDeviceStore()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "store open failed"})
		return
	}
	defer func() { _ = store.Close() }()
	if err := store.RemovePaired(r.Context(), deviceID); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "remove failed"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "removed"})
}
