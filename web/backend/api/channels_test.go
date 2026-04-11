package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
)

func TestHandleListChannelCatalog(t *testing.T) {
	h := NewHandler(filepath.Join(t.TempDir(), "config.json"))
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/channels/catalog", nil)
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var resp struct {
		Channels []channelCatalogItem `json:"channels"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if len(resp.Channels) == 0 {
		t.Fatal("channels should not be empty")
	}

	// Verify expected channels are present
	names := make(map[string]struct{}, len(resp.Channels))
	for _, ch := range resp.Channels {
		names[ch.Name] = struct{}{}
	}
	for _, expected := range []string{"telegram", "discord", "webui", "slack"} {
		if _, ok := names[expected]; !ok {
			t.Fatalf("channel %q not found in catalog: %v", expected, resp.Channels)
		}
	}
}
