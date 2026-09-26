// ClawEh
// License: MIT

package agent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/PivotLLM/ctxengine/memory"
	"github.com/PivotLLM/ctxengine/session"
	"github.com/tenebris-tech/alerter"

	"github.com/PivotLLM/ClawEh/cogmemhost"
	"github.com/PivotLLM/ClawEh/logger"
	"github.com/PivotLLM/ClawEh/routing"
)

// The nightly retention pass fires at this local time, after cogmem's default
// nightly consolidation (03:20) so a session it just read is not deleted under
// it, and away from the default backup time.
const (
	retentionHour   = 3
	retentionMinute = 45
)

// retentionReport is what one nightly pass did, for the log line and the
// failure alert.
type retentionReport struct {
	deleted     []string // session keys whose archive was removed
	skippedOpen int      // idle-old sessions kept because the loop holds them open
	snapshots   int      // cogmem pre-migration snapshots removed
	errors      []error
}

// StartRetention runs the nightly retention pass in the background until the
// loop is closed. It ticks once a minute and, when the local clock reaches
// retentionHour:retentionMinute, deletes session archives idle for longer than
// session.retention_days (read live, so a config change needs no restart) and
// cogmem pre-migration snapshots older than cogmemhost.SnapshotMaxAge. Boot
// only, like the backup scheduler: the loop outlives config reloads.
func (al *AgentLoop) StartRetention() {
	go func() {
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		lastRunDay := "" // YYYYMMDD of the last run; guards once-per-day
		for {
			select {
			case <-al.evictStop:
				return
			case now := <-ticker.C:
				if now.Hour() != retentionHour || now.Minute() != retentionMinute {
					continue
				}
				day := now.Format("20060102")
				if day == lastRunDay {
					continue
				}
				lastRunDay = day
				al.runRetentionPass(now)
			}
		}
	}()
}

// runRetentionPass performs one retention pass over every agent as of now,
// logs a summary and raises one alert when anything failed.
func (al *AgentLoop) runRetentionPass(now time.Time) retentionReport {
	days := 0
	if cfg := al.GetConfig(); cfg != nil {
		days = cfg.Session.RetentionDays
	}
	var rep retentionReport
	registry := al.GetRegistry()
	for _, agentID := range registry.ListAgentIDs() {
		ag, ok := registry.GetAgent(agentID)
		if !ok || ag == nil {
			continue
		}
		if days > 0 {
			isOpen := func(key string) bool { return al.sessionOpen(ag.ID, key) }
			r := pruneSessions(filepath.Join(ag.Workspace, "sessions"), ag.Sessions, now, days, isOpen)
			rep.deleted = append(rep.deleted, r.deleted...)
			rep.skippedOpen += r.skippedOpen
			rep.errors = append(rep.errors, r.errors...)
		}
		removed, err := cogmemhost.PruneSnapshots(cogmemhost.Dir(ag.Workspace), now, cogmemhost.SnapshotMaxAge)
		rep.snapshots += len(removed)
		if err != nil {
			rep.errors = append(rep.errors, fmt.Errorf("agent %s: %w", ag.ID, err))
		}
	}

	fields := map[string]any{
		"retention_days":     days,
		"sessions_deleted":   len(rep.deleted),
		"sessions_open_kept": rep.skippedOpen,
		"snapshots_deleted":  rep.snapshots,
		"errors":             len(rep.errors),
	}
	if len(rep.deleted) > 0 {
		fields["sessions"] = rep.deleted
	}
	switch {
	case len(rep.errors) > 0:
		fields["error"] = errors.Join(rep.errors...).Error()
		logger.ErrorCF("retention", "nightly retention pass failed", fields)
		al.Alerter().Send(alerter.Alert{
			Title:       "Session retention failed",
			Description: "some idle session archives or cogmem snapshots were not deleted; nothing retries before tomorrow night",
			Details:     errors.Join(rep.errors...).Error(),
			EventID:     "session-retention",
		})
	case len(rep.deleted) > 0 || rep.snapshots > 0 || rep.skippedOpen > 0:
		logger.InfoCF("retention", "nightly retention pass complete", fields)
	default:
		logger.DebugCF("retention", "nightly retention pass: nothing to do", fields)
	}
	return rep
}

// sessionOpen reports whether the loop holds a context manager for the
// session: in a turn, or touched within the eviction TTL and still cached.
// Either way its archive handle is open and the file must not be removed.
func (al *AgentLoop) sessionOpen(agentID, sessionKey string) bool {
	_, ok := al.contextManagers.Load(agentID + ":" + sessionKey)
	return ok
}

// retentionExempt reports whether retention must never delete key: the
// agent's main session (the shared conversation under unified scope, and the
// WebUI operator's conversation there) and its headless service session, both
// of which exist for the life of the agent and are trimmed by archive_days
// instead. Keys of an unrecognised shape are left alone too.
func retentionExempt(key string) bool {
	parsed := routing.ParseAgentSessionKey(key)
	if parsed == nil {
		return true
	}
	return parsed.Rest == routing.DefaultMainKey || parsed.Rest == "service"
}

// pruneSessions deletes every archive in dir (one agent's sessions
// directory) whose session is not exempt, has no turn pending, is not held
// open, and was last updated before now minus days. Last activity is the
// session state's UpdatedAt, which every write refreshes; a state row
// migrated without one falls back to the file's modification time. Handles
// the store caches for the session are closed before the files go.
func pruneSessions(dir string, store session.SessionStore, now time.Time, days int, isOpen func(key string) bool) retentionReport {
	var rep retentionReport
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return rep
	}
	if err != nil {
		rep.errors = append(rep.errors, fmt.Errorf("retention: %w", err))
		return rep
	}
	cutoff := now.Add(-time.Duration(days) * 24 * time.Hour)
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".archive.db") {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		a, err := memory.OpenReadOnly(path)
		if err != nil {
			continue
		}
		st, err := a.State()
		if err != nil || st.Key == "" {
			continue // unreadable, or an archive with no state row: not ours to judge
		}
		if retentionExempt(st.Key) || st.PendingTurn {
			continue
		}
		last := st.UpdatedAt
		if last.IsZero() {
			info, err := entry.Info()
			if err != nil {
				continue
			}
			last = info.ModTime()
		}
		if !last.Before(cutoff) {
			continue
		}
		if isOpen != nil && isOpen(st.Key) {
			rep.skippedOpen++
			continue
		}
		forgetSessionState(store, st.Key)
		if err := memory.DeleteSession(dir, st.Key); err != nil {
			rep.errors = append(rep.errors, fmt.Errorf("retention: %s: %w", st.Key, err))
			continue
		}
		rep.deleted = append(rep.deleted, st.Key)
	}
	return rep
}

// ReleaseSession closes everything the loop holds open for sessionKey so its
// archive can be deleted: the cached context manager (with its MCP session
// token) and the session store's handle, for whichever agent owns the key.
// It fails, deleting nothing, while a turn is in flight on the session.
func (al *AgentLoop) ReleaseSession(sessionKey string) error {
	registry := al.GetRegistry()
	for _, agentID := range registry.ListAgentIDs() {
		ag, ok := registry.GetAgent(agentID)
		if !ok || ag == nil {
			continue
		}
		if v, ok := al.contextManagers.Load(ag.ID + ":" + sessionKey); ok {
			if entry, ok := v.(*cmEntry); ok && entry.refcount.Load() > 0 {
				return fmt.Errorf("session %s has a turn in flight", sessionKey)
			}
			al.dropContextManager(context.Background(), ag, sessionKey)
		}
		forgetSessionState(ag.Sessions, sessionKey)
	}
	return nil
}
