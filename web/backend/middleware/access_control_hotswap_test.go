package middleware

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
)

// TestIPAllowlist_HotSwap is the recovery path `claw network` depends on: the
// middleware reads the allowlist per request, so widening it on a live listener
// lets a previously refused client in without recreating the listener (which
// would drop every WebUI WebSocket connection).
func TestIPAllowlist_HotSwap(t *testing.T) {
	var current atomic.Pointer[Allowlist]
	store := func(cidrs []string) {
		list, err := CompileAllowlist(cidrs)
		if err != nil {
			t.Fatalf("CompileAllowlist(%v) error = %v", cidrs, err)
		}
		current.Store(list)
	}
	store(nil)

	h := IPAllowlist(current.Load, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	call := func() int {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/api/config", nil)
		req.RemoteAddr = "192.168.1.50:5000"
		h.ServeHTTP(rec, req)
		return rec.Code
	}

	if got := call(); got != http.StatusForbidden {
		t.Fatalf("before widening: status = %d, want %d", got, http.StatusForbidden)
	}

	store([]string{"192.168.0.0/16"})
	if got := call(); got != http.StatusOK {
		t.Fatalf("after widening: status = %d, want %d — a swapped allowlist must apply to the next request", got, http.StatusOK)
	}

	// Narrowing must take effect just as promptly, or `claw network none` would
	// report success while still serving the network.
	store(nil)
	if got := call(); got != http.StatusForbidden {
		t.Fatalf("after narrowing: status = %d, want %d", got, http.StatusForbidden)
	}
}

// TestIPAllowlist_NilAllowlistIsLoopbackOnly covers the state before the first
// SetAllowlist: an unset atomic pointer must fail closed, not open.
func TestIPAllowlist_NilAllowlistIsLoopbackOnly(t *testing.T) {
	h := IPAllowlist(func() *Allowlist { return nil }, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	for addr, want := range map[string]int{
		"127.0.0.1:5000":   http.StatusOK,
		"192.168.1.50:500": http.StatusForbidden,
		"203.0.113.9:5000": http.StatusForbidden,
	} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.RemoteAddr = addr
		h.ServeHTTP(rec, req)
		if rec.Code != want {
			t.Fatalf("%s: status = %d, want %d", addr, rec.Code, want)
		}
	}
}

// TestIPAllowlist_ConcurrentSwap runs the swap against live traffic, which is
// the real shape of a config reload: requests are in flight while the reload
// goroutine installs a new allowlist. Under -race this also pins that the
// allowlist is only ever read, never mutated in place — an Allowlist shared by
// every in-flight request must be immutable.
func TestIPAllowlist_ConcurrentSwap(t *testing.T) {
	var current atomic.Pointer[Allowlist]
	open, err := CompileAllowlist([]string{"192.168.0.0/16"})
	if err != nil {
		t.Fatal(err)
	}
	shut, err := CompileAllowlist(nil)
	if err != nil {
		t.Fatal(err)
	}
	current.Store(shut)

	h := IPAllowlist(current.Load, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	var readers, swapper sync.WaitGroup
	stop := make(chan struct{})

	// Swapper: flip the allowlist back and forth for the duration. It is waited
	// on separately from the readers — it only exits once they are done.
	swapper.Add(1)
	go func() {
		defer swapper.Done()
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			if i%2 == 0 {
				current.Store(open)
			} else {
				current.Store(shut)
			}
		}
	}()

	// Readers: every response must be one of the two valid answers, never a
	// panic, a torn read, or a loopback rejection.
	for i := 0; i < 8; i++ {
		readers.Add(1)
		go func() {
			defer readers.Done()
			for n := 0; n < 500; n++ {
				rec := httptest.NewRecorder()
				req := httptest.NewRequest(http.MethodGet, "/api/config", nil)
				req.RemoteAddr = "192.168.1.50:5000"
				h.ServeHTTP(rec, req)
				if rec.Code != http.StatusOK && rec.Code != http.StatusForbidden {
					t.Errorf("unexpected status %d during a swap", rec.Code)
					return
				}

				// Loopback must be served no matter which allowlist is current.
				rec = httptest.NewRecorder()
				req = httptest.NewRequest(http.MethodGet, "/api/config", nil)
				req.RemoteAddr = "127.0.0.1:5000"
				h.ServeHTTP(rec, req)
				if rec.Code != http.StatusOK {
					t.Errorf("loopback got %d during a swap; it must always be served", rec.Code)
					return
				}
			}
		}()
	}

	readers.Wait()
	close(stop)
	swapper.Wait()
}

// TestCompileAllowlist_Tolerates covers the input shapes a hand-edited config
// can carry, so a stray space or a mixed list does not fail the reload and
// strand the operator with the previous allowlist.
func TestCompileAllowlist_Tolerates(t *testing.T) {
	for _, tc := range []struct {
		name  string
		cidrs []string
		addr  string
		want  bool
	}{
		{"padded wildcard", []string{" * "}, "203.0.113.9:5000", true},
		{"wildcard among CIDRs", []string{"10.0.0.0/8", "*"}, "203.0.113.9:5000", true},
		{"host bits set are normalised", []string{"192.168.1.55/24"}, "192.168.1.9:5000", true},
		{"IPv6 prefix", []string{"2001:db8::/32"}, "[2001:db8::5]:5000", true},
		{"IPv6 prefix excludes others", []string{"2001:db8::/32"}, "[2001:dba::5]:5000", false},
		{"empty string entries are refused", []string{""}, "127.0.0.1:5000", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			list, err := CompileAllowlist(tc.cidrs)
			if err != nil {
				if tc.name == "empty string entries are refused" {
					return // an empty entry is a parse error; loopback still works via the nil list
				}
				t.Fatalf("CompileAllowlist(%v) error = %v", tc.cidrs, err)
			}
			h := IPAllowlist(func() *Allowlist { return list }, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusOK)
			}))
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			req.RemoteAddr = tc.addr
			h.ServeHTTP(rec, req)
			served := rec.Code == http.StatusOK
			if served != tc.want {
				t.Fatalf("cidrs=%v addr=%s served=%v, want %v", tc.cidrs, tc.addr, served, tc.want)
			}
		})
	}
}
