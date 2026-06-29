package api

import (
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"

	"github.com/PivotLLM/ClawEh/pkg/channels/device"
	"github.com/PivotLLM/ClawEh/pkg/config"
	"github.com/PivotLLM/ClawEh/pkg/gatewayproto"
)

// registerDeviceRoutes wires the external-device gateway onboarding/management API.
//
// SECURITY: these endpoints expose the pairing QR (which embeds the shared gateway
// token) and the approve/reject controls. They inherit the same auth posture as the
// rest of /api/* — see web/backend/server.go. Keep them behind the operator auth the
// setup wizard adds; do not expose this API on an untrusted network.
func (h *Handler) registerDeviceRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/devices/pair", h.handleDevicePair)
	mux.HandleFunc("GET /api/devices/pending", h.handleDevicePending)
	mux.HandleFunc("POST /api/devices/pending/{id}/approve", h.handleDeviceApprove)
	mux.HandleFunc("POST /api/devices/pending/{id}/reject", h.handleDeviceReject)
	mux.HandleFunc("GET /api/devices", h.handleDeviceList)
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

// handleDevicePair returns the Rabbit R1 setup QR payload (clawdbot-gateway raw JSON).
func (h *Handler) handleDevicePair(w http.ResponseWriter, _ *http.Request) {
	cfg, err := config.LoadConfig(h.configPath)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "config load failed"})
		return
	}
	port := cfg.Gateway.Port
	if port == 0 {
		port = 18790
	}
	payload := gatewayproto.NewSetupPayload(lanIPv4s(), port, cfg.Channels.Device.Token, gatewayproto.SetupProtocolWS)
	encoded, err := payload.Encode()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "encode failed"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"payload":  encoded, // raw JSON string to render as a QR code
		"ips":      payload.IPs,
		"port":     payload.Port,
		"protocol": payload.Protocol,
		"hasToken": payload.Token != "",
		"enabled":  cfg.Channels.Device.Enabled,
	})
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
	Roles        []string `json:"roles"`
	Scopes       []string `json:"scopes"`
	ApprovedAtMs int64    `json:"approved_at_ms"`
	LastSeenAtMs int64    `json:"last_seen_at_ms"`
}

func (h *Handler) handleDeviceList(w http.ResponseWriter, r *http.Request) {
	store, _, err := h.openDeviceStore()
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
			Roles: d.Roles, Scopes: d.Scopes, ApprovedAtMs: d.ApprovedAtMs, LastSeenAtMs: d.LastSeenAtMs,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"devices": views})
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

// lanIPv4s returns routable LAN IPv4 addresses (excludes loopback, link-local, and
// the Docker default bridge), mirroring the Rabbit setup script's IP detection.
func lanIPv4s() []string {
	ifaces, err := net.Interfaces()
	if err != nil {
		return []string{}
	}
	out := []string{}
	for _, ifc := range ifaces {
		if ifc.Flags&net.FlagUp == 0 || ifc.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := ifc.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			var ip net.IP
			switch v := a.(type) {
			case *net.IPNet:
				ip = v.IP
			case *net.IPAddr:
				ip = v.IP
			}
			ip4 := ip.To4()
			if ip4 == nil {
				continue
			}
			if ip4[0] == 127 || (ip4[0] == 169 && ip4[1] == 254) || (ip4[0] == 172 && ip4[1] == 17) {
				continue
			}
			out = append(out, ip4.String())
		}
	}
	return out
}
