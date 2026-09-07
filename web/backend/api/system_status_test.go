package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// The status endpoint reports the running process, so the figures must be the
// process's own and plausible.
func TestSystemStatus(t *testing.T) {
	configPath, cleanup := setupTestEnv(t)
	defer cleanup()

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/system/status", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	var got statusResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if got.Version == "" {
		t.Error("version is empty")
	}
	if got.PID <= 0 {
		t.Errorf("pid = %d", got.PID)
	}
	if got.Goroutines <= 0 {
		t.Errorf("goroutines = %d", got.Goroutines)
	}
	if got.Uptime == "" || got.UptimeSeconds < 0 {
		t.Errorf("uptime = %q / %d", got.Uptime, got.UptimeSeconds)
	}

	// Resident set size, not virtual size. A Go process reserves over a
	// gigabyte of address space, so a gigabyte-scale answer would mean VmSize
	// had been read — the figure would be wrong in the most misleading
	// direction, suggesting the process is enormous when it is not.
	if got.MemoryBytes > 0 {
		if got.MemoryBytes < 1<<20 {
			t.Errorf("memory_bytes = %d, implausibly small for a running process", got.MemoryBytes)
		}
		if got.MemoryBytes > 2<<30 {
			t.Errorf("memory_bytes = %d — that looks like VmSize, not VmRSS", got.MemoryBytes)
		}
		if got.HeapBytes > got.MemoryBytes {
			t.Errorf("heap %d exceeds resident %d, which cannot be", got.HeapBytes, got.MemoryBytes)
		}
	}
}

// The runtime figures must not depend on config access: "is it up, and how
// big" should still answer when the configuration cannot be read.
//
// Note LoadConfig returns the DEFAULT config for a missing file rather than an
// error (config.go: os.IsNotExist -> return cfg, nil), so the counts fall back
// to the seeded defaults instead of zero. That is the established behaviour
// across the whole API, not something this endpoint decides.
func TestSystemStatusSurvivesAnUnreadableConfig(t *testing.T) {
	h := NewHandler("/nonexistent/does-not-exist.json")
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/system/status", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 even without a config", rec.Code)
	}
	var got statusResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.PID <= 0 || got.Version == "" {
		t.Errorf("runtime fields missing: %+v", got)
	}
	if got.Goroutines <= 0 || got.Uptime == "" {
		t.Errorf("runtime figures should not depend on the config: %+v", got)
	}
}

func TestCountChannels(t *testing.T) {
	configPath, cleanup := setupTestEnv(t)
	defer cleanup()
	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/system/status", nil))
	var got statusResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if got.Channels < 0 {
		t.Errorf("channels = %d", got.Channels)
	}
}
