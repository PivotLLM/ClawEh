package middleware

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestJSONContentType_SetsHeaderForAPIPath(t *testing.T) {
	called := false
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	})
	h := JSONContentType(inner)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/config", nil)
	h.ServeHTTP(rec, req)

	if !called {
		t.Fatal("inner handler not called")
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("Content-Type = %q, want application/json", ct)
	}
}

func TestJSONContentType_SkipsSSEEndpoints(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {})
	h := JSONContentType(inner)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/gateway/events", nil)
	h.ServeHTTP(rec, req)

	if ct := rec.Header().Get("Content-Type"); ct == "application/json" {
		t.Fatalf("Content-Type should not be application/json for SSE endpoint, got %q", ct)
	}
}

func TestJSONContentType_SkipsNonAPIPath(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {})
	h := JSONContentType(inner)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	h.ServeHTTP(rec, req)

	if ct := rec.Header().Get("Content-Type"); ct == "application/json" {
		t.Fatalf("Content-Type should not be set for non-API path, got %q", ct)
	}
}

func TestLogger_LogsRequest(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
	})
	h := Logger(inner)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/config", nil)
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusCreated)
	}
}

func TestLogger_DefaultsToStatus200(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Does not call WriteHeader — should default to 200
		_, _ = w.Write([]byte("ok"))
	})
	h := Logger(inner)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/models", nil)
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
}

func TestRecoverer_Returns500OnPanic(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		panic("test panic")
	})
	h := Recoverer(inner)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/config", nil)
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusInternalServerError)
	}
	if !strings.Contains(rec.Body.String(), "internal server error") {
		t.Fatalf("body = %q, want contains 'internal server error'", rec.Body.String())
	}
}

func TestRecoverer_PassesThroughOnSuccess(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	h := Recoverer(inner)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/config", nil)
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
}

func TestResponseRecorder_WriteHeaderCaptures(t *testing.T) {
	base := httptest.NewRecorder()
	rr := &responseRecorder{ResponseWriter: base, statusCode: http.StatusOK}

	rr.WriteHeader(http.StatusNotFound)

	if rr.statusCode != http.StatusNotFound {
		t.Fatalf("statusCode = %d, want %d", rr.statusCode, http.StatusNotFound)
	}
	if base.Code != http.StatusNotFound {
		t.Fatalf("underlying code = %d, want %d", base.Code, http.StatusNotFound)
	}
}

func TestResponseRecorder_UnwrapReturnsUnderlying(t *testing.T) {
	base := httptest.NewRecorder()
	rr := &responseRecorder{ResponseWriter: base, statusCode: http.StatusOK}

	if rr.Unwrap() != base {
		t.Fatal("Unwrap() should return the underlying ResponseWriter")
	}
}

func TestResponseRecorder_FlushDelegates(t *testing.T) {
	// httptest.ResponseRecorder implements http.Flusher — Flush should not panic
	base := httptest.NewRecorder()
	rr := &responseRecorder{ResponseWriter: base, statusCode: http.StatusOK}
	rr.Flush() // should not panic
}

func TestIPAllowlist_RejectsOutsideCIDR_APIPathReturnsJSON(t *testing.T) {
	h, err := IPAllowlist([]string{"192.168.1.0/24"}, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	if err != nil {
		t.Fatalf("IPAllowlist() error = %v", err)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/config", nil)
	req.RemoteAddr = "10.0.0.8:1234"
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusForbidden)
	}
	ct := rec.Header().Get("Content-Type")
	if ct != "application/json" {
		t.Fatalf("Content-Type = %q, want application/json for API path rejection", ct)
	}
	if !strings.Contains(rec.Body.String(), "access denied") {
		t.Fatalf("body = %q, want contains 'access denied'", rec.Body.String())
	}
}

func TestIPAllowlist_RejectsNonAPIPath_PlainText(t *testing.T) {
	h, err := IPAllowlist([]string{"192.168.1.0/24"}, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	if err != nil {
		t.Fatalf("IPAllowlist() error = %v", err)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "10.0.0.8:1234"
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusForbidden)
	}
}

func TestIPAllowlist_UnparsableRemoteAddr(t *testing.T) {
	h, err := IPAllowlist([]string{"192.168.1.0/24"}, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	if err != nil {
		t.Fatalf("IPAllowlist() error = %v", err)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/config", nil)
	req.RemoteAddr = "not-a-valid-ip"
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusForbidden)
	}
}
