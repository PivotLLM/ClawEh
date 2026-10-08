// ClawEh
// License: MIT

package agentreg

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
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
	Purpose      string              `json:"purpose,omitempty"`
	CloneModel   string              `json:"clone_model,omitempty"`
	Mode         Mode                `json:"mode,omitempty"`
	SystemPrompt string              `json:"system_prompt,omitempty"`
}

// persist writes the temporary agents that survive a restart (those without
// an ephemeral memory) to temp_agents.json, atomically, mode 0600. A failure
// is logged: the agents keep working, they are just not restored after a
// restart.
func (r *Registry[T]) persist() {
	if r.statePath == "" || r.stateUnusable {
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
			Purpose:    e.spec.Purpose,
			CloneModel: e.spec.CloneModel,
		}
		if !e.spec.IsClone() {
			rec.Config, rec.Mode, rec.SystemPrompt = e.spec.Config, e.spec.Mode, e.spec.SystemPrompt
		}
		file.Agents = append(file.Agents, rec)
	}
	r.mu.RUnlock()
	// Agents restore could not rebuild stay listed for the next start.
	file.Agents = append(file.Agents, r.unrestored...)

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
// configuration can no longer build is deleted with a log line; one that
// fails to build for any other reason is kept on disk and in the file for the
// next start. Every other directory under the temp root (an ephemeral agent,
// such as a sub-agent run the process stopped in the middle of, or one left
// by a crash between creating it and saving the list) is removed.
//
// A file that cannot be read or parsed, or that a newer release wrote, is
// left as it is, and so is every directory under the temp root: nothing is
// restored, nothing is deleted, and the file is not rewritten while this
// process runs.
func (r *Registry[T]) restore() {
	if r.statePath == "" {
		return
	}
	data, err := os.ReadFile(r.statePath)
	var file stateFile
	switch {
	case errors.Is(err, fs.ErrNotExist):
		// Nothing saved: every directory under the temp root is an orphan.
	case err != nil:
		r.keepSavedState(err)
		return
	default:
		if err := json.Unmarshal(data, &file); err != nil {
			r.keepSavedState(err)
			return
		}
		if file.Version > stateFileVersion {
			r.keepSavedState(fmt.Errorf("format version %d is newer than this release's %d", file.Version, stateFileVersion))
			return
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
		spec.Owner, spec.Purpose, spec.CloneModel = rec.Owner, rec.Purpose, rec.CloneModel
		spec, gone, err := r.respec(r.cfg, spec, r.entries)
		if gone != "" {
			removeDir(spec.StateDir)
			logger.InfoCF("agent", "Deleted temporary agent", map[string]any{
				"agent_id": id, "agent": spec.Label(), "reason": gone,
			})
			continue
		}
		var inst T
		if err == nil {
			inst, err = r.build(r.cfg, spec)
		}
		if err != nil {
			// Says nothing about the configuration: keep the record and the
			// directory, and try again at the next start.
			r.unrestored = append(r.unrestored, *rec)
			logger.WarnCF("agent", "Temporary agent not restored: rebuild failed; kept for the next start", map[string]any{
				"agent_id": id, "agent": spec.Label(), "error": err.Error(),
			})
			continue
		}
		m := newMeta(rec.Created, time.Duration(rec.TTLSeconds)*time.Second)
		m.touch(rec.LastUsed)
		r.entries[id] = &entry[T]{inst: inst, spec: spec, meta: m}
		restored++
	}
	r.removeOrphanDirs()
	if len(file.Agents) != restored+len(r.unrestored) {
		r.persist()
	}
	if restored > 0 {
		logger.InfoCF("agent", "Restored temporary agents", map[string]any{"count": restored})
	}
}

// keepSavedState is restore's answer to a temp_agents.json it cannot use:
// the file and every directory under the temp root are left alone, and the
// file is not rewritten while this process runs (temporary agents created
// now are not restored after a restart).
func (r *Registry[T]) keepSavedState(err error) {
	r.stateUnusable = true
	logger.WarnCF("agent", "Temporary agents not restored: their list could not be read; it and their folders are kept, and new temporary agents are not saved until it is fixed or removed",
		map[string]any{"path": r.statePath, "error": err.Error()})
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
		if slices.ContainsFunc(r.unrestored, func(rec tempRecord) bool { return rec.ID == d.Name() }) {
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
