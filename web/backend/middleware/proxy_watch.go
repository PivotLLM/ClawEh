package middleware

import (
	"net/http"
	"net/netip"
	"sync"
	"time"

	"github.com/PivotLLM/ClawEh/config"
	"github.com/PivotLLM/ClawEh/internal/clock"
	"github.com/PivotLLM/ClawEh/logger"
)

// forwardingHeaders are the headers a reverse proxy or tunnel adds to name
// the client it forwards for.
var forwardingHeaders = []string{"X-Forwarded-For", "X-Real-IP", "Forwarded"}

// ProxyWatch remembers that a request arrived from a loopback peer carrying
// forwarding headers while that peer was not a trusted proxy: the sign of a
// proxy or tunnel on this machine that gateway.trusted_proxies does not list.
// It only records; request handling is unchanged. The first sighting per
// process logs one WARN. A nil *ProxyWatch records nothing. Safe for
// concurrent use.
type ProxyWatch struct {
	// Clock is the time source for LastSeen; nil means clock.Real.
	Clock clock.Clock

	mu     sync.Mutex
	peer   string
	seenAt time.Time
}

// observe records r when its TCP peer is loopback, it carries a forwarding
// header and trusted does not trust the peer.
func (w *ProxyWatch) observe(r *http.Request, trusted *config.TrustedProxySet) {
	if w == nil || !hasForwardingHeader(r) {
		return
	}
	peer := clientIPFromRemoteAddr(r.RemoteAddr)
	if peer == nil || !peer.IsLoopback() {
		return
	}
	addr, ok := netip.AddrFromSlice(peer)
	if !ok {
		return
	}
	peerStr := addr.Unmap().String()
	if trusted.Trusts(peerStr) {
		return
	}
	now := clock.Or(w.Clock).Now()
	w.mu.Lock()
	first := w.seenAt.IsZero()
	w.peer, w.seenAt = peerStr, now
	w.mu.Unlock()
	if first {
		logger.WarnCF("http", "Forwarded request from a proxy that is not trusted; add it to gateway.trusted_proxies",
			map[string]any{"peer": peerStr, "path": r.URL.Path})
	}
}

// LastSeen returns the loopback peer and time of the most recent forwarded
// request from an untrusted peer; peer is "" when there has been none.
func (w *ProxyWatch) LastSeen() (peer string, at time.Time) {
	if w == nil {
		return "", time.Time{}
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.peer, w.seenAt
}

func hasForwardingHeader(r *http.Request) bool {
	for _, h := range forwardingHeaders {
		if r.Header.Get(h) != "" {
			return true
		}
	}
	return false
}
