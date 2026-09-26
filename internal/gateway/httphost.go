package gateway

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/PivotLLM/ClawEh/channels"
	"github.com/PivotLLM/ClawEh/config"
	"github.com/PivotLLM/ClawEh/logger"
	"github.com/PivotLLM/ClawEh/utils"
	"github.com/PivotLLM/ClawEh/web/backend/middleware"
)

// messageRoutePattern is the token-in-path message API (see RegisterMessageRoute).
// It authenticates by the token, so it is exempt from cross-origin protection.
const messageRoutePattern = "POST /api/message/{token}"

// hstsHeader is what the HTTPS listener sends when the certificate is one a
// browser trusts on its own (operator-supplied). It is never sent with a
// self-signed certificate: the browser accepted that one by hand, and an
// HSTS pin would lock it out after the certificate is regenerated.
const hstsHeader = "max-age=31536000"

// defaultMaxBody caps the request body on every route without an entry in
// bodyLimits. 1 MiB is far above any JSON the WebUI API accepts, and matches
// the caps the message route and the LINE webhook already apply themselves.
const defaultMaxBody int64 = 1 << 20

// bodyLimits are the routes that legitimately take more than defaultMaxBody,
// as path prefixes; the longest matching prefix wins. Each limit is at or
// above the handler's own cap so that, within the handler's range, the error
// the client sees is still the handler's.
var bodyLimits = []struct {
	prefix string
	limit  int64
}{
	// POST /api/memory/{id}/import takes a portable memory export, which the
	// handler caps at 32 MiB; bulk memory writes share the prefix.
	{"/api/memory/", 32 << 20},
	// POST /api/skills/import is a multipart upload whose file part the
	// handler caps at 1 MiB; the rest is form framing.
	{"/api/skills/import", 4 << 20},
}

// maxBodyFor is the request body limit for path.
func maxBodyFor(path string) int64 {
	limit, matched := defaultMaxBody, -1
	for _, b := range bodyLimits {
		if strings.HasPrefix(path, b.prefix) && len(b.prefix) > matched {
			limit, matched = b.limit, len(b.prefix)
		}
	}
	return limit
}

// limitBody refuses a request whose declared Content-Length exceeds the
// route's limit with 413 before the handler runs, and caps a body of unknown
// length at the same limit through MaxBytesReader, so a read past it fails in
// the handler and the connection is closed afterwards.
func limitBody(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		limit := maxBodyFor(r.URL.Path)
		if r.ContentLength > limit {
			http.Error(w, http.StatusText(http.StatusRequestEntityTooLarge), http.StatusRequestEntityTooLarge)
			return
		}
		if r.Body != nil && r.Body != http.NoBody {
			r.Body = http.MaxBytesReader(w, r.Body, limit)
		}
		next.ServeHTTP(w, r)
	})
}

// hostOptions says what the gateway's listeners bind and serve.
type hostOptions struct {
	// Port is the loopback HTTP port, bound on 127.0.0.1 and [::1]. Zero
	// takes an ephemeral port (tests).
	Port int
	// AllowedCIDRs is the client IP allowlist; loopback is always allowed.
	AllowedCIDRs []string
	// TLSHost, when set, opens the HTTPS listener on TLSHost:TLSPort with
	// TLSConfig. Empty means no HTTPS listener (a loopback-only gateway).
	TLSHost   string
	TLSPort   int
	TLSConfig *tls.Config
	// HSTS adds Strict-Transport-Security to HTTPS responses.
	HSTS bool
}

// httpHost owns the gateway's listeners: plain HTTP on loopback, always, and
// HTTPS on the configured host when the gateway is bound off-box. Both serve
// one handler chain and one mux. The listeners are started once at gateway
// boot and stay up across config reloads; on reload the handler mux is swapped
// atomically via SetMux, the IP allowlist via SetAllowlist, and the Host and
// cross-origin policy via ApplyPolicy. This keeps WebUI WebSocket connections,
// channel webhooks, the WebUI API, and the callback route alive while the
// channel manager and other services are rebuilt.
type httpHost struct {
	opts    hostOptions
	handler http.Handler // the shared chain, outermost first (see newHTTPHost)

	mux         atomic.Pointer[http.ServeMux]
	allow       atomic.Pointer[middleware.Allowlist]
	hosts       atomic.Pointer[middleware.HostAllowlist]
	crossOrigin atomic.Pointer[http.CrossOriginProtection]
	// certNames are the TLS certificate's names, always among the allowed
	// Hosts (a client that verified the certificate for a name expects the
	// gateway to answer to it). Set by SetCertificateNames, read by ApplyPolicy.
	certNames atomic.Pointer[[]string]
	// auth is the login-session store the Auth middleware consults (nil fails
	// closed: every protected request is 401); authExempt the routes that
	// need no session, rebuilt by ApplyPolicy since the signed webhook paths
	// come from config.
	auth       atomic.Pointer[middleware.AuthStore]
	authExempt atomic.Pointer[middleware.AuthExempt]

	// httpServer serves the loopback listeners, tlsServer the HTTPS one (nil
	// when off). Set by Start.
	httpServer *http.Server
	tlsServer  *http.Server
	// loopbackAddr and httpsAddr are the bound addresses after Start, for logs
	// and tests; httpsAddr is "" when the HTTPS listener is off.
	loopbackAddr string
	httpsAddr    string
	// onFatal hears when a listener dies after Start, with the service name
	// "http" and an error naming the address. The gateway installs its
	// fail-fast path here; nil only logs.
	onFatal func(name string, err error)
}

func newHTTPHost(opts hostOptions) (*httpHost, error) {
	h := &httpHost{opts: opts}
	// The chain in front of the dynamic mux, outermost first:
	//   IP allowlist   — who may connect. Loopback always; otherwise the
	//                    configured networks, regardless of bind address.
	//                    Matches the TCP peer (RemoteAddr), so behind a reverse
	//                    proxy enforce access control at the proxy instead.
	//   Host check     — which names this gateway answers to (DNS rebinding).
	//   Cross-origin   — no unsafe requests from another site's page (CSRF).
	//   Headers        — browser hardening on every response, 401s included.
	//   Auth           — a login session on the API, the WebUI socket and
	//                    every non-GET request; exempt routes carry their own
	//                    authentication (see middleware.AuthExemptPaths).
	//   Body limit     — request bodies capped per route (see bodyLimits).
	// Each policy is read per request through an atomic pointer so a config
	// reload can change it on a live listener. Until ApplyPolicy runs the Host
	// and cross-origin policies are loopback-only and strict respectively, and
	// until SetAuth runs every protected request is refused.
	if err := h.SetAllowlist(opts.AllowedCIDRs); err != nil {
		return nil, err
	}
	h.handler = middleware.IPAllowlist(h.allow.Load,
		middleware.HostCheck(h.hosts.Load,
			middleware.CrossOrigin(h.crossOrigin.Load,
				middleware.SecurityHeaders(
					middleware.Auth(h.auth.Load, h.authExempt.Load,
						limitBody(http.HandlerFunc(h.serveMux)))))))
	return h, nil
}

// SetAuth installs the login-session store the Auth middleware consults.
func (h *httpHost) SetAuth(s *middleware.AuthStore) { h.auth.Store(s) }

// SetAllowlist compiles cidrs and swaps the result in for subsequent requests.
// An empty list means loopback only. Invalid input returns an error and leaves
// the current allowlist untouched, so a bad edit during a config reload cannot
// widen or drop access as a side effect.
func (h *httpHost) SetAllowlist(cidrs []string) error {
	list, err := middleware.CompileAllowlist(cidrs)
	if err != nil {
		return err
	}
	h.allow.Store(list)
	return nil
}

// SetAllowedHosts swaps in the names a request's Host header may carry.
// Loopback names are always included.
func (h *httpHost) SetAllowedHosts(names []string) {
	h.hosts.Store(middleware.CompileHostAllowlist(names))
}

// SetCertificateNames records the TLS certificate's names for ApplyPolicy to
// include among the allowed Hosts. Call ApplyPolicy afterwards to apply.
func (h *httpHost) SetCertificateNames(names []string) {
	copied := append([]string(nil), names...)
	h.certNames.Store(&copied)
}

// SetCrossOrigin swaps in the cross-origin policy: trustedOrigins may make
// unsafe cross-origin requests; bypassPatterns are routes with their own
// authentication. An invalid origin returns an error and leaves the current
// policy untouched.
func (h *httpHost) SetCrossOrigin(trustedOrigins, bypassPatterns []string) error {
	policy, err := middleware.NewCrossOriginProtection(trustedOrigins, bypassPatterns)
	if err != nil {
		return err
	}
	h.crossOrigin.Store(policy)
	return nil
}

// ApplyPolicy derives the Host allowlist and cross-origin policy from the
// gateway config and the running channels, and swaps both in. Called at boot
// and on every config reload, since external_url and the LINE webhook path can
// change, and after a certificate change.
//
// Allowed hosts are the bind host, the host of the effective external URL
// (gateway.external_url when set, otherwise the advertised listener URL), the
// TLS certificate's names (SetCertificateNames) and the always-allowed
// loopback names. Anything else — a DNS-rebinding name, a hostname nobody told
// the gateway about — gets 421. gateway.external_url, when set, is also the one
// origin trusted to make cross-site requests.
func (h *httpHost) ApplyPolicy(gw config.GatewayConfig, cm *channels.Manager) error {
	var trusted []string
	if gw.ExternalURL != "" {
		u, err := url.Parse(gw.ExternalURL)
		if err != nil {
			return fmt.Errorf("invalid gateway.external_url %q: %w", gw.ExternalURL, err)
		}
		trusted = []string{u.Scheme + "://" + u.Host}
	}
	if err := h.SetCrossOrigin(trusted, append([]string{messageRoutePattern}, signedWebhookPaths(cm)...)); err != nil {
		return fmt.Errorf("invalid gateway.external_url %q: %w", gw.ExternalURL, err)
	}
	// Signed webhooks authenticate their own requests, so they need no login.
	h.authExempt.Store(middleware.CompileAuthExempt(signedWebhookPaths(cm)...))

	hosts := []string{gw.Host}
	if u, err := url.Parse(gw.EffectiveExternalURL()); err == nil {
		hosts = append(hosts, u.Hostname())
	}
	if names := h.certNames.Load(); names != nil {
		hosts = append(hosts, *names...)
	}
	h.SetAllowedHosts(hosts)
	logger.InfoCF("gateway", "HTTP host policy applied", map[string]any{
		"hosts":           hosts,
		"trusted_origins": trusted,
	})
	return nil
}

// signedWebhookPaths returns the mounted paths of channel webhooks that verify
// their own request signatures and so may be posted to from any origin. Only
// LINE (HMAC-signed) qualifies today; the WebUI channel's /webui/ is not one.
func signedWebhookPaths(cm *channels.Manager) []string {
	if cm == nil {
		return nil
	}
	ch, ok := cm.GetChannel("line")
	if !ok {
		return nil
	}
	wh, ok := ch.(channels.WebhookHandler)
	if !ok {
		return nil
	}
	return []string{wh.WebhookPath()}
}

// SetMux atomically swaps the handler mux. In-flight requests served by the
// previous mux continue to completion; subsequent requests use the new mux.
func (h *httpHost) SetMux(mux *http.ServeMux) {
	h.mux.Store(mux)
}

func (h *httpHost) serveMux(w http.ResponseWriter, r *http.Request) {
	if m := h.mux.Load(); m != nil {
		m.ServeHTTP(w, r)
		return
	}
	http.NotFound(w, r)
}

// LoopbackAddr is the bound IPv4 loopback address after Start.
func (h *httpHost) LoopbackAddr() string { return h.loopbackAddr }

// HTTPSAddr is the bound HTTPS address after Start, or "" when the HTTPS
// listener is off.
func (h *httpHost) HTTPSAddr() string { return h.httpsAddr }

// Start binds the listeners and serves them in the background. The IPv4
// loopback and HTTPS binds are required and their failure is returned; an
// unavailable [::1] is logged and skipped, since not every host has IPv6.
func (h *httpHost) Start() error {
	ln4, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(h.opts.Port)))
	if err != nil {
		return fmt.Errorf("bind loopback HTTP listener: %w", err)
	}
	h.loopbackAddr = ln4.Addr().String()
	listeners := []net.Listener{ln4}
	tcpAddr, ok := ln4.Addr().(*net.TCPAddr)
	if !ok {
		utils.CloseQuietly(ln4)
		return fmt.Errorf("bind loopback HTTP listener: unexpected address %q", ln4.Addr())
	}
	port := strconv.Itoa(tcpAddr.Port)
	if ln6, err := net.Listen("tcp", net.JoinHostPort("::1", port)); err != nil {
		logger.WarnCF("gateway", "IPv6 loopback listener unavailable; serving IPv4 loopback only", map[string]any{
			"addr": "[::1]:" + port, "error": err.Error(),
		})
	} else {
		listeners = append(listeners, ln6)
	}

	h.httpServer = &http.Server{
		Handler:      h.handler,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 30 * time.Second,
	}
	if h.opts.TLSHost != "" {
		lnTLS, err := net.Listen("tcp", net.JoinHostPort(h.opts.TLSHost, strconv.Itoa(h.opts.TLSPort)))
		if err != nil {
			for _, ln := range listeners {
				utils.CloseQuietly(ln)
			}
			return fmt.Errorf("bind HTTPS listener: %w", err)
		}
		h.httpsAddr = lnTLS.Addr().String()
		handler := h.handler
		if h.opts.HSTS {
			handler = hsts(handler)
		}
		h.tlsServer = &http.Server{
			Handler:      handler,
			TLSConfig:    h.opts.TLSConfig,
			ReadTimeout:  30 * time.Second,
			WriteTimeout: 30 * time.Second,
		}
		logger.InfoCF("gateway", "HTTPS listener bound", map[string]any{"addr": h.httpsAddr, "hsts": h.opts.HSTS})
		go h.serve(h.tlsServer, lnTLS, true)
	}
	addrs := make([]string, 0, len(listeners))
	for _, ln := range listeners {
		addrs = append(addrs, ln.Addr().String())
		go h.serve(h.httpServer, ln, false)
	}
	logger.InfoCF("gateway", "Loopback HTTP listener bound", map[string]any{"addrs": addrs})
	return nil
}

// serve runs one listener to completion and hands a listener that dies to
// onFatal. Any of the three listeners (IPv4 loopback, [::1], HTTPS) counts.
func (h *httpHost) serve(srv *http.Server, ln net.Listener, useTLS bool) {
	var err error
	if useTLS {
		err = srv.ServeTLS(ln, "", "")
	} else {
		err = srv.Serve(ln)
	}
	if err == nil || errors.Is(err, http.ErrServerClosed) {
		return
	}
	addr := ln.Addr().String()
	if h.onFatal == nil {
		logger.ErrorCF("gateway", "HTTP listener error", map[string]any{"addr": addr, "error": err.Error()})
		return
	}
	h.onFatal("http", fmt.Errorf("%s: %w", addr, err))
}

// hsts adds Strict-Transport-Security to every response of the wrapped handler.
func hsts(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Strict-Transport-Security", hstsHeader)
		next.ServeHTTP(w, r)
	})
}

// Stop shuts down the listeners. Only invoked on full gateway shutdown — never
// during a config reload.
func (h *httpHost) Stop(ctx context.Context) error {
	var errs []error
	for _, srv := range []*http.Server{h.httpServer, h.tlsServer} {
		if srv == nil {
			continue
		}
		if err := srv.Shutdown(ctx); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}
