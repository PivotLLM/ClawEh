// ClawEh
// License: MIT

package api

import (
	"context"
	"net/http"
	"time"

	"github.com/PivotLLM/ClawEh/pkg/channels/device"

	"github.com/tenebris-tech/secmsg/schema"
)

// SecMsgLinker is the slice of a running secmsg channel the WebUI pairing panel
// drives. A live *secmsg.SecMsgChannel satisfies it; the gateway injects a
// name→linker lookup via SetSecMsgLinker so this package stays decoupled from
// the channel implementation (mirrors SetMessageTokenLoop).
type SecMsgLinker interface {
	RequestLink(ctx context.Context) (*schema.LinkReply, error)
	LinkState(ctx context.Context) (*schema.LinkReply, error)
}

// SecMsgLinkerLookup resolves a configured channel name to its live linker.
type SecMsgLinkerLookup func(channelName string) (SecMsgLinker, bool)

// SetSecMsgLinker wires the channel-manager lookup so the link endpoints can
// reach the live channel instance by name. Re-injected on every gateway rebuild.
func (h *Handler) SetSecMsgLinker(fn SecMsgLinkerLookup) {
	h.reloadMu.Lock()
	h.secmsgLinker = fn
	h.reloadMu.Unlock()
}

func (h *Handler) secmsgLinkerRef() SecMsgLinkerLookup {
	h.reloadMu.Lock()
	defer h.reloadMu.Unlock()
	return h.secmsgLinker
}

func (h *Handler) registerSecMsgRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/channels/secmsg/{name}/link", h.handleSecMsgLinkRequest)
	mux.HandleFunc("GET /api/channels/secmsg/{name}/link", h.handleSecMsgLinkStatus)
}

// secmsgLinkResponse is the shape the WebUI pairing panel consumes. QRPng is a
// PNG data-URL for the pairing URI (empty once linking completes).
type secmsgLinkResponse struct {
	Status string `json:"status"`
	URI    string `json:"uri,omitempty"`
	QRPng  string `json:"qr_png,omitempty"`
	Phone  string `json:"phone,omitempty"`
	Error  string `json:"error,omitempty"`
}

// resolveSecMsgLinker looks up the live channel for the {name} path value,
// writing the appropriate error response when it cannot.
func (h *Handler) resolveSecMsgLinker(w http.ResponseWriter, r *http.Request) (SecMsgLinker, bool) {
	lookup := h.secmsgLinkerRef()
	if lookup == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "channel manager not ready"})
		return nil, false
	}
	name := r.PathValue("name")
	linker, ok := lookup(name)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "secmsg channel not found: " + name})
		return nil, false
	}
	return linker, true
}

// handleSecMsgLinkRequest starts device linking and returns the pairing QR.
//
//	POST /api/channels/secmsg/{name}/link
func (h *Handler) handleSecMsgLinkRequest(w http.ResponseWriter, r *http.Request) {
	linker, ok := h.resolveSecMsgLinker(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	reply, err := linker.RequestLink(ctx)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, secmsgLinkReply(reply))
}

// handleSecMsgLinkStatus reports current pairing status for polling.
//
//	GET /api/channels/secmsg/{name}/link
func (h *Handler) handleSecMsgLinkStatus(w http.ResponseWriter, r *http.Request) {
	linker, ok := h.resolveSecMsgLinker(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	reply, err := linker.LinkState(ctx)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, secmsgLinkReply(reply))
}

// secmsgLinkReply maps a daemon LinkReply to the WebUI shape, rendering the
// pairing URI to a QR PNG data-URL (server-side, matching the device pairing
// flow) so the frontend needs no QR library.
func secmsgLinkReply(reply *schema.LinkReply) secmsgLinkResponse {
	out := secmsgLinkResponse{
		Status: reply.Status,
		URI:    reply.URI,
		Phone:  reply.Phone,
		Error:  reply.Error,
	}
	if reply.URI != "" {
		if png, err := device.RenderQRCodePNGDataURL(reply.URI); err == nil {
			out.QRPng = png
		}
	}
	return out
}
