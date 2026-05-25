package api

import (
	"strings"
	"testing"

	"github.com/PivotLLM/ClawEh/pkg/config"
)

// hasErr returns true if any error string contains substr.
func hasErr(errs []string, substr string) bool {
	for _, e := range errs {
		if strings.Contains(e, substr) {
			return true
		}
	}
	return false
}

func newAgentCfg(id, workspace, memoryDir string) config.AgentConfig {
	return config.AgentConfig{ID: id, Workspace: workspace, MemoryDir: memoryDir}
}

func TestValidateAgentMemoryDirs_AcceptsEmpty(t *testing.T) {
	cfg := &config.Config{}
	cfg.Agents.List = []config.AgentConfig{
		newAgentCfg("alice", "/tmp/ws-a", ""),
		newAgentCfg("bob", "/tmp/ws-b", ""),
	}
	if errs := validateAgentMemoryDirs(cfg); len(errs) != 0 {
		t.Fatalf("expected no errors, got %v", errs)
	}
}

func TestValidateAgentMemoryDirs_RejectsRelative(t *testing.T) {
	cfg := &config.Config{}
	cfg.Agents.List = []config.AgentConfig{
		newAgentCfg("alice", "/tmp/ws-a", "relative/path"),
	}
	errs := validateAgentMemoryDirs(cfg)
	if !hasErr(errs, "must be an absolute path") {
		t.Fatalf("expected absolute-path error, got %v", errs)
	}
}

func TestValidateAgentMemoryDirs_RejectsNULByte(t *testing.T) {
	cfg := &config.Config{}
	cfg.Agents.List = []config.AgentConfig{
		newAgentCfg("alice", "/tmp/ws-a", "/tmp/mem\x00bad"),
	}
	errs := validateAgentMemoryDirs(cfg)
	if !hasErr(errs, "NUL bytes") {
		t.Fatalf("expected NUL byte error, got %v", errs)
	}
}

func TestValidateAgentMemoryDirs_RejectsAncestorOfOwnWorkspace(t *testing.T) {
	cfg := &config.Config{}
	cfg.Agents.List = []config.AgentConfig{
		// memory_dir is an ancestor of its own workspace: forbidden.
		newAgentCfg("alice", "/tmp/ws/a", "/tmp/ws"),
	}
	errs := validateAgentMemoryDirs(cfg)
	if !hasErr(errs, "ancestor of its own workspace") {
		t.Fatalf("expected ancestor-of-own-workspace error, got %v", errs)
	}
}

func TestValidateAgentMemoryDirs_RejectsAncestorOfOtherWorkspace(t *testing.T) {
	cfg := &config.Config{}
	cfg.Agents.List = []config.AgentConfig{
		newAgentCfg("alice", "/tmp/ws-a", "/tmp/ws-b"), // equals bob's workspace
		newAgentCfg("bob", "/tmp/ws-b", ""),
	}
	errs := validateAgentMemoryDirs(cfg)
	if !hasErr(errs, "ancestor of agent bob workspace") {
		t.Fatalf("expected cross-agent ancestor error, got %v", errs)
	}
}

func TestValidateAgentMemoryDirs_RejectsSharedMemoryDir(t *testing.T) {
	cfg := &config.Config{}
	cfg.Agents.List = []config.AgentConfig{
		newAgentCfg("alice", "/tmp/ws-a", "/tmp/shared-memory"),
		newAgentCfg("bob", "/tmp/ws-b", "/tmp/shared-memory"),
	}
	errs := validateAgentMemoryDirs(cfg)
	if !hasErr(errs, "must not share a memory directory") {
		t.Fatalf("expected shared-memory error, got %v", errs)
	}
	// Make sure we only emit the shared message once, not twice.
	count := 0
	for _, e := range errs {
		if strings.Contains(e, "must not share a memory directory") {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("expected exactly 1 shared-memory error, got %d: %v", count, errs)
	}
}

func TestValidateAgentMemoryDirs_TildeExpansion(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	cfg := &config.Config{}
	cfg.Agents.List = []config.AgentConfig{
		// "~/private/memory" expands to under HOME — must be accepted.
		newAgentCfg("alice", "/tmp/ws-a", "~/private/memory"),
	}
	if errs := validateAgentMemoryDirs(cfg); len(errs) != 0 {
		t.Fatalf("expected no errors, got %v", errs)
	}
}

func TestValidateAgentMemoryDirs_AcceptsDisjointPaths(t *testing.T) {
	cfg := &config.Config{}
	cfg.Agents.List = []config.AgentConfig{
		newAgentCfg("alice", "/tmp/ws-a", "/tmp/mem-a"),
		newAgentCfg("bob", "/tmp/ws-b", "/tmp/mem-b"),
	}
	if errs := validateAgentMemoryDirs(cfg); len(errs) != 0 {
		t.Fatalf("expected no errors for disjoint paths, got %v", errs)
	}
}
