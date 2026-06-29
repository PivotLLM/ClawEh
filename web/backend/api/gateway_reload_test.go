package api

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHandleGatewayReload(t *testing.T) {
	post := func(h *Handler) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		h.handleGatewayReload(rec, httptest.NewRequest(http.MethodPost, "/api/gateway/reload", nil))
		return rec
	}

	t.Run("no trigger wired", func(t *testing.T) {
		if got := post(NewHandler("")).Code; got != http.StatusServiceUnavailable {
			t.Fatalf("status = %d, want 503", got)
		}
	})

	t.Run("trigger succeeds", func(t *testing.T) {
		h := NewHandler("")
		called := false
		h.SetReloadTrigger(func() error { called = true; return nil })
		if got := post(h).Code; got != http.StatusOK {
			t.Fatalf("status = %d, want 200", got)
		}
		if !called {
			t.Fatal("reload trigger was not invoked")
		}
	})

	t.Run("trigger fails", func(t *testing.T) {
		h := NewHandler("")
		h.SetReloadTrigger(func() error { return errors.New("boom") })
		if got := post(h).Code; got != http.StatusInternalServerError {
			t.Fatalf("status = %d, want 500", got)
		}
	})
}
