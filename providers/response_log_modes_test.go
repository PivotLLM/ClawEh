//go:build !windows

package providers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/PivotLLM/ClawEh/config"
	"github.com/PivotLLM/ClawEh/internal/perms"
)

// A model's response_log_file under <CLAW_HOME>/logs, and the folder created
// for it, take the logs area's configured modes, for both HTTP protocols that
// write one. The umask is 0 so the test shows the modes ClawEh passes.
func TestResponseLog_ConfiguredLogsModes(t *testing.T) {
	old := syscall.Umask(0)
	defer syscall.Umask(old)
	home := t.TempDir()
	t.Setenv("CLAW_HOME", home)
	cfg := config.DefaultConfig()
	cfg.Security.Permissions.Logs = config.AreaPermissions{DirMode: "0750", FileMode: "0640"}
	perms.SetPolicy(perms.PolicyFor(cfg))
	t.Cleanup(func() { perms.SetPolicy(perms.Policy{}) })

	cases := []struct {
		protocol string
		body     string
	}{
		{"openai-chat", `{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`},
		{"openai-responses", `{"status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}]}]}`},
	}
	for _, c := range cases {
		t.Run(c.protocol, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if _, err := w.Write([]byte(c.body)); err != nil {
					t.Errorf("write response: %v", err)
				}
			}))
			defer srv.Close()

			dir := filepath.Join(home, "logs", c.protocol)
			logFile := filepath.Join(dir, "responses.log")
			model := &config.ModelConfig{ModelName: "m", Model: "m", Provider: "p", ResponseLogFile: logFile}
			prov := &config.Provider{Name: "p", Protocol: c.protocol, BaseURL: srv.URL, APIKey: "k"}
			provider, modelID, err := CreateProviderFromConfig(model, prov)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := provider.Chat(context.Background(), []Message{{Role: "user", Content: "hi"}}, nil, modelID, nil); err != nil {
				t.Fatalf("Chat: %v", err)
			}
			for p, want := range map[string]os.FileMode{dir: 0o750, logFile: 0o640} {
				fi, err := os.Stat(p)
				if err != nil {
					t.Fatal(err)
				}
				if got := fi.Mode().Perm(); got != want {
					t.Errorf("%s = %04o, want %04o", p, got, want)
				}
			}
		})
	}
}
