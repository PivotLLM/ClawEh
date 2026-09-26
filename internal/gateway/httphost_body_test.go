package gateway

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/PivotLLM/ClawEh/config"
)

// bodyHost is a policy host whose mux records whether a handler ran and how
// many body bytes it could read, on every route.
func bodyHost(t *testing.T) (*httpHost, *atomic.Int32, *atomic.Int64, *http.Cookie) {
	t.Helper()
	h := newPolicyHost(t, config.GatewayConfig{Host: "127.0.0.1", Port: 18790})
	var reached atomic.Int32
	var readBytes atomic.Int64
	var readErr atomic.Pointer[error]
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		reached.Add(1)
		n, err := io.Copy(io.Discard, r.Body)
		readBytes.Store(n)
		if err != nil {
			readErr.Store(&err)
			if mbe, ok := errors.AsType[*http.MaxBytesError](err); ok {
				w.Header().Set("X-Body-Limit", strconv.FormatInt(mbe.Limit, 10))
				w.WriteHeader(http.StatusRequestEntityTooLarge)
				return
			}
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
	h.SetMux(mux)
	return h, &reached, &readBytes, loginCookie(t, h)
}

func postBody(h *httpHost, path string, body io.Reader, cookie *http.Cookie) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, path, body)
	req.RemoteAddr = "127.0.0.1:5000"
	req.Host = "127.0.0.1:18790"
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(cookie)
	h.handler.ServeHTTP(rec, req)
	return rec
}

// TestBodyLimit_OversizedAPIBodyIs413: a JSON body over 1 MiB to an ordinary
// API route is refused before the handler runs.
func TestBodyLimit_OversizedAPIBodyIs413(t *testing.T) {
	h, reached, _, cookie := bodyHost(t)
	big := bytes.NewReader([]byte(`{"pad":"` + strings.Repeat("x", int(defaultMaxBody)) + `"}`))
	rec := postBody(h, "/api/config", big, cookie)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusRequestEntityTooLarge)
	}
	if reached.Load() != 0 {
		t.Fatal("handler ran for an oversized body")
	}
}

// TestBodyLimit_UnknownLengthIsCappedInHandler: with no Content-Length the
// handler runs, but its read fails with MaxBytesError at the limit.
func TestBodyLimit_UnknownLengthIsCappedInHandler(t *testing.T) {
	h, reached, readBytes, cookie := bodyHost(t)
	// io.MultiReader hides the length from httptest.NewRequest (ContentLength -1).
	big := io.MultiReader(strings.NewReader(strings.Repeat("x", int(defaultMaxBody)+1)))
	rec := postBody(h, "/api/config", big, cookie)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want %d (MaxBytesError in the handler)", rec.Code, http.StatusRequestEntityTooLarge)
	}
	if got := rec.Header().Get("X-Body-Limit"); got != strconv.FormatInt(defaultMaxBody, 10) {
		t.Fatalf("MaxBytesError limit = %q, want %d", got, defaultMaxBody)
	}
	if reached.Load() != 1 {
		t.Fatal("handler did not run for a body of unknown length")
	}
	if n := readBytes.Load(); n > defaultMaxBody {
		t.Fatalf("handler read %d bytes, more than the %d limit", n, defaultMaxBody)
	}
}

// TestBodyLimit_WithinLimitReachesHandler: a body under the cap is untouched.
func TestBodyLimit_WithinLimitReachesHandler(t *testing.T) {
	h, reached, readBytes, cookie := bodyHost(t)
	rec := postBody(h, "/api/config", strings.NewReader(`{"ok":true}`), cookie)
	if rec.Code != http.StatusOK || reached.Load() != 1 || readBytes.Load() != int64(len(`{"ok":true}`)) {
		t.Fatalf("status %d, reached %d, read %d", rec.Code, reached.Load(), readBytes.Load())
	}
}

// TestBodyLimit_UploadRoutesTakeMore: the routes in bodyLimits accept a body
// larger than the default, up to their own cap.
func TestBodyLimit_UploadRoutesTakeMore(t *testing.T) {
	h, reached, readBytes, cookie := bodyHost(t)
	twoMiB := int64(2 << 20)
	for _, path := range []string{"/api/memory/main/import", "/api/skills/import"} {
		reached.Store(0)
		rec := postBody(h, path, bytes.NewReader(make([]byte, twoMiB)), cookie)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: status = %d, want %d for a 2 MiB body", path, rec.Code, http.StatusOK)
		}
		if reached.Load() != 1 || readBytes.Load() != twoMiB {
			t.Fatalf("%s: reached %d, read %d, want the whole 2 MiB", path, reached.Load(), readBytes.Load())
		}
	}
	// Still bounded: over the route's own cap is refused.
	rec := postBody(h, "/api/skills/import", bytes.NewReader(make([]byte, (4<<20)+1)), cookie)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("/api/skills/import over its cap: status = %d, want %d", rec.Code, http.StatusRequestEntityTooLarge)
	}
}

// TestMaxBodyFor pins the route table.
func TestMaxBodyFor(t *testing.T) {
	for path, want := range map[string]int64{
		"/api/config":                    defaultMaxBody,
		"/api/message/tok":               defaultMaxBody,
		"/":                              defaultMaxBody,
		"/api/skills":                    defaultMaxBody,
		"/api/skills/import":             4 << 20,
		"/api/memory/main/import":        32 << 20,
		"/api/memory/main/bulk":          32 << 20,
		"/api/memory":                    defaultMaxBody,
		"/api/memory/main/memories/abc1": 32 << 20,
	} {
		if got := maxBodyFor(path); got != want {
			t.Errorf("maxBodyFor(%q) = %d, want %d", path, got, want)
		}
	}
}
