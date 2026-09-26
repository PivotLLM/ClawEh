// ClawEh
// License: MIT

package agent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/PivotLLM/ctxengine"
	"github.com/PivotLLM/ctxengine/session"

	"github.com/PivotLLM/ClawEh/providers"
)

// TestCompress_E2E_ClaudeCLIReceivesFortification locks in the full compression
// dispatch chain: Manager.doCompress → compressModelCaller.Complete →
// providerLLMClient.chat → ClaudeCliProvider.Chat. The provider's stdin must
// carry the JSON-object fortification because the caller passes the engine's
// JSONObject request through as ResponseFormatJSONObjectOption. Without that wiring this test fails — see the
// mutation evidence captured in the worker report.
func TestCompress_E2E_ClaudeCLIReceivesFortification(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("mock CLI scripts not supported on Windows")
	}

	dir := t.TempDir()
	stdinFile := filepath.Join(dir, "stdin.txt")
	script := filepath.Join(dir, "cli")
	body := fmt.Sprintf(`#!/bin/sh
cat - > '%s'
cat <<'EOFMOCK'
{"type":"result","result":"{\"version\":2,\"state\":{\"goals\":[{\"text\":\"ok\",\"refs\":[{\"seq_start\":1}]}]}}","session_id":"t"}
EOFMOCK
`, stdinFile)
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}

	cli := providers.NewClaudeCliProvider(script, t.TempDir(), nil, nil)
	client := &providerLLMClient{provider: cli, model: "claude-cli", requestJSONObject: true}

	sessionKey := "e2e-compress"
	store, err := session.NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	defer closeT(t, store)
	// Six distinct messages large enough that selectTail cannot retain them all
	// at the default 20% retain budget against a 1000-token context window;
	// the older half is handed to the compression LLM (i.e. the mock CLI).
	for i := range 6 {
		role := "user"
		if i%2 == 1 {
			role = "assistant"
		}
		addMessage(t, store, sessionKey, role,
			fmt.Sprintf("msg %d payload %s", i, strings.Repeat("token ", 200)))
	}

	cm := ctxengine.New(
		sessionKey,
		store,
		ctxengine.WithContextWindow(1000),
		ctxengine.WithModelCaller(&compressModelCaller{clients: []*providerLLMClient{client}}),
	)
	if compactErr := cm.Compact(context.Background()); compactErr != nil {
		t.Fatalf("Compact: %v", compactErr)
	}

	raw, err := os.ReadFile(stdinFile)
	if err != nil {
		t.Fatalf("read mock CLI stdin: %v", err)
	}
	got := string(raw)
	if !strings.Contains(got, providers.JSONObjectFortification) {
		t.Fatalf("claude-cli stdin missing JSONObjectFortification; got:\n%s", got)
	}
	if !strings.Contains(got, "msg 0 payload") {
		t.Fatalf("claude-cli stdin missing conversation content (first message); got:\n%s", got)
	}
}
