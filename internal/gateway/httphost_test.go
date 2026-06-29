package gateway

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/PivotLLM/ClawEh/pkg/bus"
	"github.com/PivotLLM/ClawEh/pkg/channels"
	"github.com/PivotLLM/ClawEh/pkg/config"
)

// TestHTTPHostLifecycleIndependentOfChannelManager asserts that the shared
// HTTP listener (owned by httpHost) is not torn down by the lifecycle of a
// channels.Manager. This is the load-bearing invariant from investigation
// 7a5377d9 / option #1: a config reload must rebuild the channel manager
// without bouncing the listener that backs WebUI WebSockets, the API, and
// channel webhooks.
func TestHTTPHostLifecycleIndependentOfChannelManager(t *testing.T) {
	addr := reserveLocalAddr(t)

	host, err := newHTTPHost(addr, nil)
	if err != nil {
		t.Fatalf("newHTTPHost: %v", err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/ping", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("pong"))
	})
	host.SetMux(mux)
	host.Start()

	t.Cleanup(func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = host.Stop(shutdownCtx)
	})

	waitForServer(t, addr, 2*time.Second)

	// Independently construct + stop a channels.Manager. It must not affect
	// the listener owned by the gateway-level httpHost.
	cfg := config.DefaultConfig()
	cm, err := channels.NewManager(cfg, bus.NewMessageBus(), nil)
	if err != nil {
		t.Fatalf("channels.NewManager: %v", err)
	}
	if err := cm.StopAll(context.Background()); err != nil {
		t.Fatalf("cm.StopAll returned error: %v", err)
	}

	// Listener must still serve traffic after the channel manager is gone.
	if got := httpGetBody(t, "http://"+addr+"/ping"); got != "pong" {
		t.Fatalf("GET /ping after ChannelManager.StopAll: body = %q, want %q (listener was torn down — option #1 regression)", got, "pong")
	}
}

// TestHTTPHostSwapMux verifies SetMux installs a new handler atomically
// without disturbing the listener — the in-flight invariant for the reload
// path. After swap, requests are served by the new mux.
func TestHTTPHostSwapMux(t *testing.T) {
	addr := reserveLocalAddr(t)

	host, err := newHTTPHost(addr, nil)
	if err != nil {
		t.Fatalf("newHTTPHost: %v", err)
	}
	first := http.NewServeMux()
	first.HandleFunc("/v", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("first"))
	})
	host.SetMux(first)
	host.Start()
	t.Cleanup(func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = host.Stop(shutdownCtx)
	})

	waitForServer(t, addr, 2*time.Second)

	if got := httpGetBody(t, "http://"+addr+"/v"); got != "first" {
		t.Fatalf("before swap: got %q, want %q", got, "first")
	}

	second := http.NewServeMux()
	second.HandleFunc("/v", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("second"))
	})
	host.SetMux(second)

	if got := httpGetBody(t, "http://"+addr+"/v"); got != "second" {
		t.Fatalf("after swap: got %q, want %q (mux swap did not take effect)", got, "second")
	}
}

// TestNewHTTPHost_EnforcesAllowlist verifies the listener wraps its mux in the
// IP allowlist: loopback and in-range peers reach the handler; out-of-range
// peers get 403 — regardless of bind address.
func TestNewHTTPHost_EnforcesAllowlist(t *testing.T) {
	host, err := newHTTPHost("127.0.0.1:0", []string{"192.168.0.0/16"})
	if err != nil {
		t.Fatalf("newHTTPHost: %v", err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/x", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	host.SetMux(mux)

	cases := []struct {
		remoteAddr string
		want       int
	}{
		{"127.0.0.1:5000", http.StatusOK},      // loopback always allowed
		{"192.168.1.10:5000", http.StatusOK},   // in private range
		{"8.8.8.8:5000", http.StatusForbidden}, // public, blocked
	}
	for _, c := range cases {
		req := httptest.NewRequest(http.MethodGet, "/x", nil)
		req.RemoteAddr = c.remoteAddr
		rec := httptest.NewRecorder()
		host.server.Handler.ServeHTTP(rec, req)
		if rec.Code != c.want {
			t.Errorf("%s: status = %d, want %d", c.remoteAddr, rec.Code, c.want)
		}
	}
}

func TestNewHTTPHost_InvalidCIDR(t *testing.T) {
	if _, err := newHTTPHost("127.0.0.1:0", []string{"not-a-cidr"}); err == nil {
		t.Fatal("expected error for invalid CIDR")
	}
}

// reserveLocalAddr binds a 127.0.0.1:0 listener, captures the OS-assigned
// address, then closes it so the caller can rebind. Cheap way to get a free
// port for an http.Server without racing against another listener.
func reserveLocalAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen: %v", err)
	}
	addr := ln.Addr().String()
	if err := ln.Close(); err != nil {
		t.Fatalf("ln.Close: %v", err)
	}
	return addr
}

func waitForServer(t *testing.T, addr string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", addr, 100*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("server at %s did not accept within %v", addr, timeout)
}

func httpGetBody(t *testing.T, url string) string {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return string(body)
}
