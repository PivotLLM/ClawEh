package api

import (
	"net"
	"net/http"
	"strconv"
	"strings"

	"github.com/PivotLLM/ClawEh/pkg/config"
)

func (h *Handler) gatewayHostOverride() string {
	// When the gateway binds to all interfaces, advertise the wildcard so
	// buildWsURL falls back to the request host (a reachable off-box address).
	if h.serverPublic {
		return "0.0.0.0"
	}
	return ""
}

func (h *Handler) effectiveGatewayBindHost(cfg *config.Config) string {
	if override := h.gatewayHostOverride(); override != "" {
		return override
	}
	if cfg == nil {
		return ""
	}
	return strings.TrimSpace(cfg.Gateway.Host)
}

func gatewayProbeHost(bindHost string) string {
	if bindHost == "" || bindHost == "0.0.0.0" {
		return "127.0.0.1"
	}
	return bindHost
}

func requestHostName(r *http.Request) string {
	reqHost, _, err := net.SplitHostPort(r.Host)
	if err == nil {
		return reqHost
	}
	if strings.TrimSpace(r.Host) != "" {
		return r.Host
	}
	return "127.0.0.1"
}

func (h *Handler) buildWsURL(r *http.Request, cfg *config.Config) string {
	host := h.effectiveGatewayBindHost(cfg)
	if host == "" || host == "0.0.0.0" {
		host = requestHostName(r)
	}
	return "ws://" + net.JoinHostPort(host, strconv.Itoa(cfg.Gateway.Port)) + "/webui/ws"
}
