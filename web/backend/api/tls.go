// ClawEh
// License: MIT

package api

import (
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/PivotLLM/ClawEh/config"
	"github.com/PivotLLM/ClawEh/internal/tlscert"
	"github.com/PivotLLM/ClawEh/logger"
)

// The TLS page of the WebUI: where the two listeners are, which certificate
// HTTPS presents, whether saved listener settings wait for a restart, a
// pre-save check of an operator certificate pair, and regeneration of the
// self-signed one. Saving the settings themselves goes through PATCH
// /api/config (see saveValidatedConfig for the certificate check there).

// tlsCertificateJSON describes a certificate. Present is false when there is
// none yet (a self-signed pair is generated on the first HTTPS start); Error
// says why an existing file could not be read.
type tlsCertificateJSON struct {
	Present     bool     `json:"present"`
	Subject     string   `json:"subject"`
	Names       []string `json:"names"`
	NotAfter    string   `json:"not_after"`
	Fingerprint string   `json:"fingerprint"`
	SelfSigned  bool     `json:"self_signed"`
	Error       string   `json:"error,omitempty"`
}

// tlsURLs are the addresses to open: plain HTTP on this machine, plain HTTP
// on the network (empty unless gateway.host is off loopback), and HTTPS.
type tlsURLs struct {
	Localhost string   `json:"localhost"`
	HTTP      []string `json:"http"`
	HTTPS     []string `json:"https"`
}

// tlsStatusResponse is GET /api/tls. Every setting is the saved one;
// RestartRequired says the running listeners were bound from different ones.
type tlsStatusResponse struct {
	Mode            string              `json:"mode"`
	Source          string              `json:"source"`
	CertFile        string              `json:"cert_file"`
	KeyFile         string              `json:"key_file"`
	ExtraNames      []string            `json:"extra_names"`
	TLSPort         int                 `json:"tls_port"`
	HTTPHost        string              `json:"http_host"`
	HTTPPort        int                 `json:"http_port"`
	ExternalURL     string              `json:"external_url"`
	URLs            tlsURLs             `json:"urls"`
	Certificate     *tlsCertificateJSON `json:"certificate"`
	RestartRequired bool                `json:"restart_required"`
}

// SetBootListeners records the listener settings the running gateway bound
// at start; GET /api/tls compares the saved config against them. Unset (no
// gateway, as in tests) means no restart is ever reported.
func (h *Handler) SetBootListeners(s config.ListenerSettings) {
	h.reloadMu.Lock()
	h.bootListeners = &s
	h.reloadMu.Unlock()
}

// SetTLSManager hands the handler the running HTTPS certificate manager; nil
// when HTTPS is off, which POST /api/tls/regenerate reports as 503.
func (h *Handler) SetTLSManager(m *tlscert.Manager) {
	h.reloadMu.Lock()
	h.tlsManager = m
	h.reloadMu.Unlock()
}

func (h *Handler) tlsState() (*config.ListenerSettings, *tlscert.Manager) {
	h.reloadMu.Lock()
	defer h.reloadMu.Unlock()
	return h.bootListeners, h.tlsManager
}

func (h *Handler) registerTLSRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/tls", h.handleGetTLS)
	mux.HandleFunc("POST /api/tls/validate", h.handleValidateTLS)
	mux.HandleFunc("POST /api/tls/regenerate", h.handleRegenerateTLS)
}

// certificateJSON renders a loaded certificate.
func certificateJSON(info tlscert.Info) *tlsCertificateJSON {
	names := info.Names()
	if names == nil {
		names = []string{}
	}
	return &tlsCertificateJSON{
		Present:     true,
		Subject:     info.Subject,
		Names:       names,
		NotAfter:    info.NotAfter.UTC().Format(time.RFC3339),
		Fingerprint: info.Fingerprint,
		SelfSigned:  info.SelfSigned,
	}
}

// handleGetTLS reports the saved listener and certificate settings, the URLs
// they give, the certificate and whether a restart is pending. The
// certificate is the one the running listener serves; with no HTTPS listener
// running it is the one the saved settings point at.
//
//	GET /api/tls
func (h *Handler) handleGetTLS(w http.ResponseWriter, _ *http.Request) {
	cfg, err := h.currentConfig()
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to load config: "+err.Error())
		return
	}
	gw := cfg.Gateway
	boot, mgr := h.tlsState()
	opts := tlscert.OptionsFromConfig(cfg)

	httpHost := strings.TrimSpace(gw.Host)
	if httpHost == "" {
		httpHost = "127.0.0.1"
	}
	resp := tlsStatusResponse{
		Mode:        gw.TLS.EffectiveMode(),
		Source:      string(opts.Source()),
		CertFile:    strings.TrimSpace(gw.TLS.CertFile),
		KeyFile:     strings.TrimSpace(gw.TLS.KeyFile),
		ExtraNames:  append([]string{}, gw.TLS.ExtraNames...),
		TLSPort:     gw.EffectiveTLSPort(),
		HTTPHost:    httpHost,
		HTTPPort:    gw.EffectivePort(),
		ExternalURL: gw.ExternalURL,
		URLs: tlsURLs{
			Localhost: gw.LocalHTTPURL(),
			HTTP:      append([]string{}, gw.NetworkHTTPURLs()...),
			HTTPS:     append([]string{}, gw.HTTPSURLs()...),
		},
		RestartRequired: boot != nil && *boot != gw.Listeners(),
	}
	if mgr != nil {
		resp.Certificate = certificateJSON(mgr.Info())
	} else {
		info, inspectErr := tlscert.InspectFile(opts)
		switch {
		case inspectErr == nil:
			resp.Certificate = certificateJSON(info)
		case errors.Is(inspectErr, fs.ErrNotExist):
			resp.Certificate = &tlsCertificateJSON{Names: []string{}}
		default:
			resp.Certificate = &tlsCertificateJSON{Names: []string{}, Error: inspectErr.Error()}
		}
	}
	writeJSON(w, http.StatusOK, resp)
}

// handleValidateTLS checks an operator certificate pair before the WebUI
// saves its paths: both files readable by the service user, the key matching
// the certificate, the certificate currently valid. It never writes config.
// A good pair is answered with the certificate object GET /api/tls carries.
//
//	POST /api/tls/validate {"cert_file": "...", "key_file": "..."}
func (h *Handler) handleValidateTLS(w http.ResponseWriter, r *http.Request) {
	var body struct {
		CertFile string `json:"cert_file"`
		KeyFile  string `json:"key_file"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	info, err := tlscert.ValidatePair(body.CertFile, body.KeyFile, time.Now())
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, certificateJSON(info))
}

// handleRegenerateTLS replaces the self-signed certificate of the running
// HTTPS listener, for the names in the saved config. The new certificate is
// served at once and its names join the accepted Host names (the manager's
// change hook re-applies the host policy). The answer is the new
// certificate object.
//
//	POST /api/tls/regenerate
func (h *Handler) handleRegenerateTLS(w http.ResponseWriter, _ *http.Request) {
	_, mgr := h.tlsState()
	if mgr == nil {
		writeJSONError(w, http.StatusServiceUnavailable,
			"no HTTPS listener is running (gateway.tls.mode was \"off\" at start); enable HTTPS and restart first")
		return
	}
	if mgr.Source() == tlscert.SourceFile {
		writeJSONError(w, http.StatusConflict,
			"the HTTPS listener uses your certificate files; only the self-signed certificate can be regenerated")
		return
	}
	cfg, err := h.currentConfig()
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to load config: "+err.Error())
		return
	}
	var externalHost string
	if u, parseErr := url.Parse(cfg.Gateway.ExternalURL); parseErr == nil {
		externalHost = u.Hostname()
	}
	mgr.UpdateNames(cfg.Gateway.TLS.ExtraNames, externalHost)
	// The FQDN lookup inside Regenerate is bounded by its own timeout; it is
	// also reached from `claw tls`, which has no context.
	if err := mgr.Regenerate(); err != nil { //nolint:contextcheck // see above
		logger.ErrorCF("tls", "Self-signed certificate regeneration from the WebUI failed", map[string]any{"error": err.Error()})
		writeJSONError(w, http.StatusInternalServerError, "regenerate failed: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, certificateJSON(mgr.Info()))
}

// validateCertificateChange checks the operator certificate pair when a save
// changes the HTTPS listener's certificate files or mode, so the WebUI cannot
// save paths the gateway would refuse to start on. A save that leaves them
// as they were is not re-checked: an expiring certificate must not block
// unrelated edits. It returns "" when there is nothing wrong.
func validateCertificateChange(before, after config.ListenerSettings) string {
	if after.CertFile == "" {
		return ""
	}
	if before.CertFile == after.CertFile && before.KeyFile == after.KeyFile && before.TLSMode == after.TLSMode {
		return ""
	}
	if _, err := tlscert.ValidatePair(after.CertFile, after.KeyFile, time.Now()); err != nil {
		return "gateway.tls: " + err.Error()
	}
	return ""
}
