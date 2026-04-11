package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/PivotLLM/ClawEh/pkg/config"
)

func TestHandleGatewayStop_WhenNotRunning(t *testing.T) {
	resetGatewayTestState(t)

	configPath := filepath.Join(t.TempDir(), "config.json")
	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/gateway/stop", nil)
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if body["status"] != "not_running" {
		t.Fatalf("status = %v, want not_running", body["status"])
	}
}

func TestHandleGatewayStop_WhenRunning(t *testing.T) {
	resetGatewayTestState(t)

	configPath := filepath.Join(t.TempDir(), "config.json")
	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	cmd := startLongRunningProcess(t)
	t.Cleanup(func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		_ = cmd.Wait()
	})

	gateway.mu.Lock()
	gateway.cmd = cmd
	gateway.bootDefaultModel = "test-model"
	setGatewayRuntimeStatusLocked("running")
	gateway.mu.Unlock()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/gateway/stop", nil)
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if body["status"] != "ok" {
		t.Fatalf("status = %v, want ok", body["status"])
	}
	if _, ok := body["pid"]; !ok {
		t.Fatal("pid should be in response")
	}
}

func TestHandleGatewayStart_PreconditionFailedWhenNoConfig(t *testing.T) {
	resetGatewayTestState(t)

	// Use a config with no default model
	configPath := filepath.Join(t.TempDir(), "config.json")
	minimalCfg := `{"agents":{"defaults":{"model":null}}}`
	if err := os.WriteFile(configPath, []byte(minimalCfg), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/gateway/start", nil)
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}

	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if body["status"] != "precondition_failed" {
		t.Fatalf("status = %v, want precondition_failed", body["status"])
	}
}

func TestHandleGatewayStart_AlreadyRunningReturnsConflict(t *testing.T) {
	resetGatewayTestState(t)

	configPath := filepath.Join(t.TempDir(), "config.json")
	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	cmd := startLongRunningProcess(t)
	t.Cleanup(func() {
		gateway.mu.Lock()
		if gateway.cmd == cmd {
			gateway.cmd = nil
			gateway.bootDefaultModel = ""
		}
		gateway.mu.Unlock()
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		_ = cmd.Wait()
	})

	gateway.mu.Lock()
	gateway.cmd = cmd
	gateway.bootDefaultModel = "test-model"
	setGatewayRuntimeStatusLocked("running")
	gateway.mu.Unlock()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/gateway/start", nil)
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusConflict, rec.Body.String())
	}

	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if body["status"] != "already_running" {
		t.Fatalf("status = %v, want already_running", body["status"])
	}
}

func TestHandleGatewayStart_StartFailsWhenBinaryInvalid(t *testing.T) {
	resetGatewayTestState(t)

	configPath := filepath.Join(t.TempDir(), "config.json")
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.SetDefaultModel(cfg.ModelList[0].ModelName)
	cfg.ModelList[0].APIKey = "test-key"
	if err := config.SaveConfig(configPath, cfg); err != nil {
		t.Fatalf("SaveConfig() error = %v", err)
	}

	// Point to a non-executable file so exec.Command fails
	invalidBinaryPath := filepath.Join(t.TempDir(), "fake-claw")
	if err := os.WriteFile(invalidBinaryPath, []byte("#!/bin/sh\n"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	t.Setenv("CLAW_BINARY", invalidBinaryPath)

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/gateway/start", nil)
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusInternalServerError, rec.Body.String())
	}
}

func TestScanPipe_ReadsLines(t *testing.T) {
	buf := NewLogBuffer(10)
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("Pipe() error = %v", err)
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		scanPipe(r, buf)
	}()

	_, _ = w.WriteString("line one\nline two\n")
	w.Close()
	<-done

	lines, total, _ := buf.LinesSince(0)
	if total != 2 {
		t.Fatalf("total = %d, want 2", total)
	}
	if len(lines) != 2 || lines[0] != "line one" || lines[1] != "line two" {
		t.Fatalf("lines = %v, want [line one, line two]", lines)
	}
}

func TestGatewayStatusOnHealthFailureLocked_ErrorAfterRunning(t *testing.T) {
	resetGatewayTestState(t)

	gateway.mu.Lock()
	setGatewayRuntimeStatusLocked("running")
	gateway.mu.Unlock()

	gateway.mu.Lock()
	status := gatewayStatusOnHealthFailureLocked()
	gateway.mu.Unlock()

	if status != "running" {
		t.Fatalf("status = %q, want running", status)
	}
}

func TestGatewayStatusOnHealthFailureLocked_ErrorStatus(t *testing.T) {
	resetGatewayTestState(t)

	gateway.mu.Lock()
	setGatewayRuntimeStatusLocked("error")
	gateway.mu.Unlock()

	gateway.mu.Lock()
	status := gatewayStatusOnHealthFailureLocked()
	gateway.mu.Unlock()

	if status != "error" {
		t.Fatalf("status = %q, want error", status)
	}
}
