// ClawEh - Personal AI Assistant
// License: MIT
//
// Copyright (c) 2026 PicoClaw contributors

package agent

import (
	"os"
	"path/filepath"

	"github.com/PivotLLM/ClawEh/pkg/fileutil"
	"github.com/PivotLLM/ClawEh/pkg/logger"
)

// MemoryStore reads the agent's curated long-term memory file, MEMORY.md, at the
// workspace root. MEMORY.md is human-authored and injected into the agent prompt;
// learned memory lives in cognitive memory (cogmem), not in files.
type MemoryStore struct {
	workspace  string
	memoryFile string
}

// NewMemoryStore creates a MemoryStore for the workspace's MEMORY.md.
func NewMemoryStore(workspace string) *MemoryStore {
	return &MemoryStore{
		workspace:  workspace,
		memoryFile: filepath.Join(workspace, "MEMORY.md"),
	}
}

// ReadLongTerm reads MEMORY.md, returning "" if the file does not exist.
func (ms *MemoryStore) ReadLongTerm() string {
	data, err := os.ReadFile(ms.memoryFile)
	if err == nil {
		return string(data)
	}
	if !os.IsNotExist(err) {
		logger.WarnCF("agent", "memory: failed to read long-term memory",
			map[string]any{"path": ms.memoryFile, "error": err.Error()})
	}
	return ""
}

// WriteLongTerm overwrites MEMORY.md atomically.
func (ms *MemoryStore) WriteLongTerm(content string) error {
	// 0o600: owner read/write only. Atomic write with sync for flash reliability.
	return fileutil.WriteFileAtomic(ms.memoryFile, []byte(content), 0o600)
}

// GetMemoryContext returns MEMORY.md formatted for the agent prompt, or "" when
// there is no long-term memory file.
func (ms *MemoryStore) GetMemoryContext() string {
	longTerm := ms.ReadLongTerm()
	if longTerm == "" {
		return ""
	}
	return "## Long-term Memory\n\n" + longTerm
}
