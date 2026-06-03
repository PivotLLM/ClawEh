// ClawEh
// License: MIT

package session

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/PivotLLM/ClawEh/pkg/tools"
)

func TestSessionClearTool(t *testing.T) {
	var gotKey, gotMsg string
	clear := func(_ context.Context, sessionKey, message string) error {
		gotKey, gotMsg = sessionKey, message
		return nil
	}
	tool := NewSessionClearTool(clear)

	ctx := tools.WithSessionKey(context.Background(), "sess-1")
	res := tool.Execute(ctx, map[string]any{"message": "do task B"})
	if res.IsError {
		t.Fatalf("unexpected error: %s", res.ForLLM)
	}
	if gotKey != "sess-1" || gotMsg != "do task B" {
		t.Errorf("clear called with (%q, %q), want (sess-1, do task B)", gotKey, gotMsg)
	}
	if !strings.Contains(res.ForLLM, "End your turn") {
		t.Errorf("result should tell the agent to end its turn: %q", res.ForLLM)
	}

	// Missing session key.
	if r := tool.Execute(context.Background(), nil); !r.IsError {
		t.Error("expected error without a session key")
	}

	// Clear callback error is surfaced (e.g. rate-limited).
	failing := NewSessionClearTool(func(context.Context, string, string) error {
		return errors.New("rate-limited")
	})
	r := failing.Execute(tools.WithSessionKey(context.Background(), "s"), nil)
	if !r.IsError || !strings.Contains(r.ForLLM, "rate-limited") {
		t.Errorf("expected the clear error surfaced, got %q (isErr=%v)", r.ForLLM, r.IsError)
	}
}
