package gateway

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

// TestHTTPHostSetAllowlist covers what makes `claw network` work against a
// running gateway: the reload path swaps the allowlist on the live listener,
// and a rejected list leaves the running one in place rather than failing open
// or silently dropping to loopback.
func TestHTTPHostSetAllowlist(t *testing.T) {
	h, err := newHTTPHost("127.0.0.1:0", nil)
	if err != nil {
		t.Fatalf("newHTTPHost() error = %v", err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	h.SetMux(mux)

	call := func() int {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.RemoteAddr = "192.168.1.50:5000"
		h.server.Handler.ServeHTTP(rec, req)
		return rec.Code
	}

	if got := call(); got != http.StatusForbidden {
		t.Fatalf("empty allowlist: status = %d, want %d (loopback only)", got, http.StatusForbidden)
	}

	if err := h.SetAllowlist([]string{"192.168.0.0/16"}); err != nil {
		t.Fatalf("SetAllowlist() error = %v", err)
	}
	if got := call(); got != http.StatusOK {
		t.Fatalf("after SetAllowlist: status = %d, want %d", got, http.StatusOK)
	}

	if err := h.SetAllowlist([]string{"not-a-cidr"}); err == nil {
		t.Fatal("SetAllowlist() accepted an invalid CIDR")
	}
	if got := call(); got != http.StatusOK {
		t.Fatalf("after rejected SetAllowlist: status = %d, want %d — a bad list must leave the running one alone", got, http.StatusOK)
	}
}

// TestHTTPHostAllowlistOverRealListener drives the whole stack the gateway
// actually serves — a net/http server with the allowlist wrapper and the
// swappable mux — over a real TCP connection, so nothing here depends on
// calling the handler directly.
func TestHTTPHostAllowlistOverRealListener(t *testing.T) {
	h, err := newHTTPHost("127.0.0.1:0", nil)
	if err != nil {
		t.Fatalf("newHTTPHost() error = %v", err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	h.SetMux(mux)

	srv := httptest.NewServer(h.server.Handler)
	defer srv.Close()

	// The test client connects over loopback, which is always allowed, so this
	// proves the wrapper does not break the ordinary path.
	resp, err := srv.Client().Get(srv.URL + "/health")
	if err != nil {
		t.Fatalf("GET error = %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("loopback status = %d, want %d", resp.StatusCode, http.StatusOK)
	}

	// And an unknown route still 404s rather than being swallowed by the
	// allowlist wrapper.
	resp, err = srv.Client().Get(srv.URL + "/nope")
	if err != nil {
		t.Fatalf("GET error = %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown route status = %d, want %d", resp.StatusCode, http.StatusNotFound)
	}
}

// TestHTTPHostSetAllowlistConcurrent mirrors what a config reload does to a busy
// gateway: the allowlist is replaced while requests are being served. Run under
// -race this is the guard against ever going back to a mutable shared allowlist.
func TestHTTPHostSetAllowlistConcurrent(t *testing.T) {
	h, err := newHTTPHost("127.0.0.1:0", nil)
	if err != nil {
		t.Fatalf("newHTTPHost() error = %v", err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	h.SetMux(mux)

	var readers, writer sync.WaitGroup
	stop := make(chan struct{})

	writer.Add(1)
	go func() {
		defer writer.Done()
		lists := [][]string{nil, {"192.168.0.0/16"}, {"*"}, {"10.0.0.0/8"}}
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			if err := h.SetAllowlist(lists[i%len(lists)]); err != nil {
				t.Errorf("SetAllowlist() error = %v", err)
				return
			}
		}
	}()

	for i := 0; i < 4; i++ {
		readers.Add(1)
		go func() {
			defer readers.Done()
			for n := 0; n < 500; n++ {
				rec := httptest.NewRecorder()
				req := httptest.NewRequest(http.MethodGet, "/", nil)
				req.RemoteAddr = "192.168.1.50:5000"
				h.server.Handler.ServeHTTP(rec, req)
				if rec.Code != http.StatusOK && rec.Code != http.StatusForbidden {
					t.Errorf("unexpected status %d during a reload", rec.Code)
					return
				}
			}
		}()
	}

	readers.Wait()
	close(stop)
	writer.Wait()
}
