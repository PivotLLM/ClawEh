// ClawEh
// License: MIT

package agentreg

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/PivotLLM/ClawEh/config"
	"github.com/PivotLLM/ClawEh/fileutil"
	"github.com/PivotLLM/ClawEh/logger"
	"github.com/PivotLLM/ClawEh/routing"
)

// stateFileVersion is the format of temp_agents.json.
const stateFileVersion = 1

// stateFile is temp_agents.json.
type stateFile struct {
	Version int          `json:"version"`
	Agents  []tempRecord `json:"agents"`
}

// tempRecord is one temporary agent that survives a restart. Its directories
// derive from its id. A clone stores only its source: its configuration is
// always its source's current one. A fresh agent stores the configuration it
// was created with, its mode and its system prompt.
type tempRecord struct {
	ID           string              `json:"id"`
	SourceID     string              `json:"source_id,omitempty"`
	Created      time.Time           `json:"created"`
	LastUsed     time.Time           `json:"last_used"`
	TTLSeconds   int64               `json:"ttl_seconds"`
	Config       *config.AgentConfig `json:"config,omitempty"`
	Owner        string              `json:"owner,omitempty"`
	Mode         Mode                `json:"mode,omitempty"`
	SystemPrompt string              `json:"system_prompt,omitempty"`
}

// persist writes the temporary agents that survive a restart (those without
// an ephemeral memory) to temp_agents.json, atomically, mode 0600. A failure
// is logged: the agents keep working, they are just not restored after a
// restart.
func (r *Registry[T]) persist() {
	if r.statePath == "" {
		return
	}
	r.persistMu.Lock()
	defer r.persistMu.Unlock()

	r.mu.RLock()
	file := stateFile{Version: stateFileVersion, Agents: []tempRecord{}}
	for _, id := range r.tempIDsLocked() {
		e := r.entries[id]
		if !persisted(e.spec) {
			continue
		}
		rec := tempRecord{
			ID:         e.spec.ID,
			SourceID:   e.spec.SourceID,
			Created:    e.meta.created.UTC(),
			LastUsed:   e.meta.last().UTC(),
			TTLSeconds: int64(e.meta.ttl / time.Second),
			Owner:      e.spec.Owner,
		}
		if !e.spec.IsClone() {
			rec.Config, rec.Mode, rec.SystemPrompt = e.spec.Config, e.spec.Mode, e.spec.SystemPrompt
		}
		file.Agents = append(file.Agents, rec)
	}
	r.mu.RUnlock()

	data, err := json.MarshalIndent(file, "", "  ")
	if err == nil {
		err = fileutil.WriteFileAtomic(r.statePath, data, 0o600)
	}
	if err != nil {
		logger.WarnCF("agent", "Failed to save temporary agents", map[string]any{
			"path": r.statePath, "error": err.Error(),
		})
	}
}

// restore rebuilds the temporary agents listed in temp_agents.json. One the
// configuration can no longer build is deleted with a log line. Every other
// directory under the temp root (an ephemeral agent, such as a sub-agent run
// the process stopped in the middle of, or one left by a crash between
// creating it and saving the list) is removed.
func (r *Registry[T]) restore() {
	if r.statePath == "" {
		return
	}
	data, err := os.ReadFile(r.statePath)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		logger.WarnCF("agent", "Failed to read temporary agents; starting without them",
			map[string]any{"path": r.statePath, "error": err.Error()})
	}
	var file stateFile
	if len(data) > 0 {
		if err := json.Unmarshal(data, &file); err != nil {
			logger.WarnCF("agent", "Failed to parse temporary agents; starting without them",
				map[string]any{"path": r.statePath, "error": err.Error()})
			file = stateFile{}
		}
	}

	restored := 0
	for i := range file.Agents {
		rec := &file.Agents[i]
		id := routing.NormalizeAgentID(rec.ID)
		fresh := rec.SourceID == ""
		if id != rec.ID || r.entries[id] != nil ||
			(fresh && (rec.Config == nil || !rec.Mode.valid() || strings.TrimSpace(rec.SystemPrompt) == "")) {
			logger.WarnCF("agent", "Ignoring invalid temporary agent record", map[string]any{"agent_id": rec.ID})
			continue
		}
		spec := Spec{ID: id, Origin: OriginTemp, SourceID: rec.SourceID, StateDir: filepath.Join(r.tempRoot, id)}
		if fresh {
			spec = freshSpec(*rec.Config, id, spec.StateDir, rec.Mode, rec.SystemPrompt, false)
		}
		spec.Owner = rec.Owner
		spec, reason := r.respec(r.cfg, spec, r.entries)
		if reason == "" {
			inst, err := r.build(r.cfg, spec)
			if err == nil {
				m := newMeta(rec.Created, time.Duration(rec.TTLSeconds)*time.Second)
				m.touch(rec.LastUsed)
				r.entries[id] = &entry[T]{inst: inst, spec: spec, meta: m}
				restored++
				continue
			}
			reason = "rebuild failed: " + err.Error()
		}
		removeDir(spec.StateDir)
		logger.InfoCF("agent", "Deleted temporary agent", map[string]any{
			"agent_id": id, "agent": spec.Label(), "reason": reason,
		})
	}
	r.removeOrphanDirs()
	if len(file.Agents) != restored {
		r.persist()
	}
	if restored > 0 {
		logger.InfoCF("agent", "Restored temporary agents", map[string]any{"count": restored})
	}
}

// removeOrphanDirs removes every directory under the temp root that no
// registered temporary agent owns.
func (r *Registry[T]) removeOrphanDirs() {
	dirs, err := os.ReadDir(r.tempRoot)
	if err != nil {
		return // no temp root yet
	}
	for _, d := range dirs {
		if e, ok := r.entries[d.Name()]; ok && e.spec.Origin == OriginTemp {
			continue
		}
		path := filepath.Join(r.tempRoot, d.Name())
		if err := os.RemoveAll(path); err != nil {
			logger.WarnCF("agent", "Failed to remove orphaned temporary agent directory",
				map[string]any{"path": path, "error": err.Error()})
			continue
		}
		logger.InfoCF("agent", "Removed temporary agent directory not saved for restart", map[string]any{"path": path})
	}
}
