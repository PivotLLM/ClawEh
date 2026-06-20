// ClawEh
// License: MIT
//
// Copyright (c) 2026 Tenebris Technologies Inc.

package common

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PivotLLM/ClawEh/pkg/config"
	"github.com/PivotLLM/ClawEh/pkg/global"
	"github.com/PivotLLM/ClawEh/pkg/tools"
)

// newDeps builds a global.Deps with a config whose common dir is commonDir and a
// ToolDeps for an agent rooted at workspace with the given per-agent config.
func newDeps(t *testing.T, commonDir, workspace string, agentCfg *config.AgentConfig) global.Deps {
	t.Helper()
	cfg := &config.Config{}
	cfg.Agents.CommonDir = commonDir
	return global.Deps{
		Cfg:  cfg,
		Host: tools.ToolDeps{Workspace: workspace, AgentCfg: agentCfg},
	}
}

// findTool returns the named bare tool definition, or fails.
func findTool(t *testing.T, defs []global.ToolDefinition, name string) global.ToolDefinition {
	t.Helper()
	for _, d := range defs {
		if d.Name == name {
			return d
		}
	}
	t.Fatalf("tool %q not registered", name)
	return global.ToolDefinition{}
}

func call(t *testing.T, d global.ToolDefinition, args map[string]any) *global.Result {
	t.Helper()
	res, err := d.Handler(&global.ToolCall{Ctx: context.Background(), Args: args})
	if err != nil {
		t.Fatalf("handler returned error: %v", err)
	}
	return res
}

// TestPutListGetDelete exercises the full round trip: put a workspace file into
// common, list shows it, get copies it back into workspace/files, delete removes it.
func TestPutListGetDelete(t *testing.T) {
	commonDir := t.TempDir()
	workspace := t.TempDir()

	// Seed a file in the agent workspace.
	srcPath := filepath.Join(workspace, "notes.txt")
	if err := os.WriteFile(srcPath, []byte("hello common"), 0o644); err != nil {
		t.Fatal(err)
	}

	defs := GlobalProvider.RegisterTools(newDeps(t, commonDir, workspace, nil))
	put := findTool(t, defs, "put")
	list := findTool(t, defs, "list")
	get := findTool(t, defs, "get")
	del := findTool(t, defs, "delete")

	// put workspace -> common
	if res := call(t, put, map[string]any{"path": "notes.txt"}); res.IsError {
		t.Fatalf("put failed: %s", res.ForLLM)
	}
	if _, err := os.Stat(filepath.Join(commonDir, "notes.txt")); err != nil {
		t.Fatalf("expected file copied into common: %v", err)
	}

	// list shows it
	res := call(t, list, map[string]any{})
	if res.IsError || !strings.Contains(res.ForLLM, "notes.txt") {
		t.Fatalf("list did not show notes.txt: %q (err=%v)", res.ForLLM, res.IsError)
	}

	// get common -> workspace/files
	if res := call(t, get, map[string]any{"name": "notes.txt"}); res.IsError {
		t.Fatalf("get failed: %s", res.ForLLM)
	}
	data, err := os.ReadFile(filepath.Join(workspace, "files", "notes.txt"))
	if err != nil {
		t.Fatalf("expected file in workspace/files: %v", err)
	}
	if string(data) != "hello common" {
		t.Errorf("content = %q, want 'hello common'", string(data))
	}

	// get with a renamed destination, creating an intermediate dir.
	if res := call(t, get, map[string]any{"name": "notes.txt", "as": "sub/renamed.txt"}); res.IsError {
		t.Fatalf("get as failed: %s", res.ForLLM)
	}
	if _, err := os.Stat(filepath.Join(workspace, "files", "sub", "renamed.txt")); err != nil {
		t.Fatalf("expected renamed file under files/sub: %v", err)
	}

	// delete removes it
	if res := call(t, del, map[string]any{"name": "notes.txt"}); res.IsError {
		t.Fatalf("delete failed: %s", res.ForLLM)
	}
	if _, err := os.Stat(filepath.Join(commonDir, "notes.txt")); !os.IsNotExist(err) {
		t.Fatalf("expected file removed from common, stat err=%v", err)
	}
}

// TestPutCreatesCommonDir verifies put creates the common dir on first write.
func TestPutCreatesCommonDir(t *testing.T) {
	parent := t.TempDir()
	commonDir := filepath.Join(parent, "nonexistent-common")
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "a.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	defs := GlobalProvider.RegisterTools(newDeps(t, commonDir, workspace, nil))
	if res := call(t, findTool(t, defs, "put"), map[string]any{"path": "a.txt"}); res.IsError {
		t.Fatalf("put failed: %s", res.ForLLM)
	}
	if _, err := os.Stat(filepath.Join(commonDir, "a.txt")); err != nil {
		t.Fatalf("expected common dir created with file: %v", err)
	}
}

// TestAbsentWhenShareCommonFalse verifies the provider returns no tools for an
// agent that disables sharing.
func TestAbsentWhenShareCommonFalse(t *testing.T) {
	off := false
	defs := GlobalProvider.RegisterTools(newDeps(t, t.TempDir(), t.TempDir(), &config.AgentConfig{ShareCommon: &off}))
	if defs != nil {
		t.Fatalf("expected no tools when ShareCommon=false, got %d", len(defs))
	}
}

// TestPresentWhenShareCommonDefault verifies the tools are present when the
// per-agent toggle is unset (default ON).
func TestPresentWhenShareCommonDefault(t *testing.T) {
	defs := GlobalProvider.RegisterTools(newDeps(t, t.TempDir(), t.TempDir(), &config.AgentConfig{}))
	if len(defs) != 4 {
		t.Fatalf("expected 4 tools by default, got %d", len(defs))
	}
}

// TestEscapeRejected verifies "../" escapes are rejected on every common-side path.
func TestEscapeRejected(t *testing.T) {
	commonDir := t.TempDir()
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "a.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	defs := GlobalProvider.RegisterTools(newDeps(t, commonDir, workspace, nil))

	cases := []struct {
		tool string
		args map[string]any
	}{
		{"list", map[string]any{"subdir": "../escape"}},
		{"get", map[string]any{"name": "../escape.txt"}},
		{"get", map[string]any{"name": "a.txt", "as": "../escape.txt"}},
		{"put", map[string]any{"path": "a.txt", "as": "../escape.txt"}},
		{"put", map[string]any{"path": "../../etc/passwd"}},
		{"delete", map[string]any{"name": "../escape.txt"}},
	}
	for _, tc := range cases {
		res := call(t, findTool(t, defs, tc.tool), tc.args)
		if !res.IsError {
			t.Errorf("%s with %v: expected escape to be rejected", tc.tool, tc.args)
		}
	}
}
