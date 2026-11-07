package tools

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type mockSessionScopedTool struct {
	mockRegistryTool
}

func (m *mockSessionScopedTool) IsSessionScoped() bool { return true }

func TestExecuteUnattended(t *testing.T) {
	r := NewToolRegistry()
	r.Register(newMockTool("ok_tool", "ok"))
	failing := newMockTool("bad_tool", "bad")
	failing.result = ErrorResult("the mailbox is locked")
	r.Register(failing)
	r.Register(&mockSessionScopedTool{mockRegistryTool: *newMockTool("session_tool", "s")})

	out, err := r.ExecuteUnattended(context.Background(), "ok_tool", nil)
	if err != nil || out != "ok" {
		t.Errorf("ok_tool = %q, %v", out, err)
	}
	if _, err = r.ExecuteUnattended(context.Background(), "missing", nil); !errors.Is(err, ErrToolNotRegistered) {
		t.Errorf("missing = %v, want ErrToolNotRegistered", err)
	}
	if _, err = r.ExecuteUnattended(context.Background(), "session_tool", nil); !errors.Is(err, ErrToolSessionScoped) {
		t.Errorf("session_tool = %v, want ErrToolSessionScoped", err)
	}
	_, err = r.ExecuteUnattended(context.Background(), "bad_tool", nil)
	if err == nil || !strings.Contains(err.Error(), `tool "bad_tool" failed: the mailbox is locked`) {
		t.Errorf("bad_tool = %v", err)
	}
}
