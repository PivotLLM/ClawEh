// ClawEh
// License: MIT

// Package folderwatch polls notify-enabled allowed folders and tells each
// owning agent (on its default channel, cron-style) when a new file appears.
package folderwatch

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/tenebris-tech/alerter"

	"github.com/PivotLLM/ClawEh/agentreg"
	"github.com/PivotLLM/ClawEh/bus"
	"github.com/PivotLLM/ClawEh/config"
	"github.com/PivotLLM/ClawEh/global"
	"github.com/PivotLLM/ClawEh/internal/perms"
	"github.com/PivotLLM/ClawEh/logger"
)

// A marker records the set of file paths the watcher has already seen in one
// allowed folder for one agent: markerHeader, the folder's path, then one path
// per line relative to the folder. A file present now but absent from the set
// is "new"; an edited or appended file is not (its path is already recorded).
// It lives in the agent's own state, <workspace>/state/folderwatch/, never in
// the watched folder: two agents watching one folder each keep their own, and
// a read-only folder needs no write. It is rewritten only on baseline and when
// a new file fires, so the scan adds almost no churn.
const markerHeader = "# claw seen files v1"

// markerDir is the folder under an agent's workspace holding its markers.
var markerDir = filepath.Join("state", "folderwatch")

// markerPath is the marker of folder (cleaned, absolute) for the agent
// agentID whose workspace is workspace. The name hashes the agent ID with the
// folder, so agents sharing a workspace still keep one marker each.
func markerPath(workspace, agentID, folder string) string {
	sum := sha256.Sum256([]byte(agentID + "\x00" + folder))
	return filepath.Join(workspace, markerDir, hex.EncodeToString(sum[:16])+".txt")
}

// markerName matches a marker's file name: 32 hex digits and ".txt".
var markerName = regexp.MustCompile(`^[0-9a-f]{32}\.txt$`)

// State is what the watchers of one process remember between scans: the
// (agent, folder) pairs watched on the latest scan. The gateway builds a new
// Watcher on every config reload and hands it the same State, so a folder
// that a reload adds (or whose notices it turns back on) is recognised as
// new, and its old marker is not read: it baselines instead. Before the
// process's first scan nothing is known, and existing markers are read, so
// files added while ClawEh was stopped are announced.
type State struct {
	mu      sync.Mutex
	scanned bool
	pairs   map[string]bool
}

// NewState returns the State of a process that has not scanned yet.
func NewState() *State { return &State{} }

// pairKey names an (agent, folder) pair in State.pairs.
func pairKey(agentID, folder string) string { return agentID + "\x00" + folder }

// Watcher periodically scans every notify-enabled allowed folder across all
// agents.
type Watcher struct {
	cfg      func() *config.Config
	bus      *bus.MessageBus
	interval time.Duration
	alerter  alerter.Alerter
	state    *State
	stop     chan struct{}
	stopOnce sync.Once
	wg       sync.WaitGroup
}

// New builds a Watcher. interval <= 0 uses global.FolderNotifyIntervalSeconds.
// a receives operator alerts for scan and marker failures; nil means none.
// state is shared by the process's watchers (see State); nil starts a new one.
func New(cfgGetter func() *config.Config, b *bus.MessageBus, interval time.Duration, a alerter.Alerter, state *State) *Watcher {
	if interval <= 0 {
		interval = time.Duration(global.FolderNotifyIntervalSeconds) * time.Second
	}
	if a == nil {
		a = alerter.Nop{}
	}
	if state == nil {
		state = NewState()
	}
	return &Watcher{cfg: cfgGetter, bus: b, interval: interval, alerter: a, state: state, stop: make(chan struct{})}
}

func (w *Watcher) Start() {
	w.wg.Add(1)
	go w.run()
}

// Stop halts the scan loop and waits for it to exit. It is safe to call more
// than once: the gateway's shutdown path runs on every config reload as well as
// at exit, and a second close of the stop channel would panic the process
// rather than log anything.
func (w *Watcher) Stop() {
	if w == nil {
		return
	}
	w.stopOnce.Do(func() { close(w.stop) })
	w.wg.Wait()
}

func (w *Watcher) run() {
	defer w.wg.Done()
	t := time.NewTicker(w.interval)
	defer t.Stop()
	for {
		select {
		case <-w.stop:
			return
		case <-t.C:
			w.tick()
		}
	}
}

// notifyFolders returns agent's folders with notices on, cleaned and without
// repeats, skipping a path that is not absolute or holds a line break
// (config.IgnoredAllowedFolders lists it).
func notifyFolders(agent *config.AgentConfig) []string {
	var out []string
	seen := map[string]bool{}
	for _, f := range agent.AllowedFolders {
		path := strings.TrimSpace(f.Path)
		if !f.Notify || !filepath.IsAbs(path) || strings.ContainsAny(path, "\n\r") {
			continue
		}
		path = filepath.Clean(path)
		if !seen[path] {
			seen[path] = true
			out = append(out, path)
		}
	}
	return out
}

func (w *Watcher) tick() {
	cfg := w.cfg()
	if cfg == nil {
		return
	}
	w.state.mu.Lock()
	defer w.state.mu.Unlock()
	pairs := map[string]bool{}
	// The marker files each workspace should hold: those of every agent
	// using it, for each folder it watches now.
	wanted := map[string]map[string]bool{}
	for i := range cfg.Agents.List {
		agent := &cfg.Agents.List[i]
		ws := agentreg.ConfigWorkspace(agent, cfg.BaseDir())
		if wanted[ws] == nil {
			wanted[ws] = map[string]bool{}
		}
		for _, path := range notifyFolders(agent) {
			marker := markerPath(ws, agent.ID, path)
			wanted[ws][filepath.Base(marker)] = true
			key := pairKey(agent.ID, path)
			pairs[key] = true
			if info, serr := os.Stat(path); serr != nil || !info.IsDir() {
				continue
			}
			// A pair the previous scan of this process did not watch starts
			// over: its old marker is from an earlier time it was watched.
			fresh := w.state.scanned && !w.state.pairs[key]
			w.scanFolder(cfg, agent, path, marker, fresh)
		}
	}
	// Two spellings of one workspace (a trailing slash, a linked alias) are
	// one marker folder: merge them by the folder's real path, after the
	// scans have created it, so one clean-up pass sees every agent's markers
	// and none deletes another's.
	byDir := map[string]map[string]bool{}
	for ws, names := range wanted {
		dir := realDir(filepath.Join(ws, markerDir))
		if byDir[dir] == nil {
			byDir[dir] = map[string]bool{}
		}
		for name := range names {
			byDir[dir][name] = true
		}
	}
	for dir, names := range byDir {
		removeStaleMarkers(dir, names)
	}
	w.state.scanned, w.state.pairs = true, pairs
}

// realDir is dir with its links resolved, or cleaned when it does not exist.
func realDir(dir string) string {
	if real, err := filepath.EvalSymlinks(dir); err == nil {
		return real
	}
	return filepath.Clean(dir)
}

// removeStaleMarkers deletes the markers in dir that keep is missing: those of
// folders no longer watched, so watching one again starts with a new list.
func removeStaleMarkers(dir string, keep map[string]bool) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		name := e.Name()
		if !e.Type().IsRegular() || !markerName.MatchString(name) || keep[name] {
			continue
		}
		if rerr := os.Remove(filepath.Join(dir, name)); rerr != nil && !os.IsNotExist(rerr) {
			logger.WarnCF("folderwatch", "Failed to remove the file list of a folder no longer watched",
				map[string]any{"path": filepath.Join(dir, name), "error": rerr.Error()})
		}
	}
}

// scanFolder detects new files in one folder via the agent's marker for it
// and notifies the owning agent for each. fresh ignores the marker.
func (w *Watcher) scanFolder(cfg *config.Config, agent *config.AgentConfig, folder, marker string, fresh bool) {
	for _, file := range w.detectNewFiles(agent.ID, agent.DisplayName(), folder, marker, fresh) {
		logger.InfoCF("folderwatch", "new file detected in allowed folder", map[string]any{
			"agent_id": agent.ID,
			"folder":   folder,
			"file":     file,
		})
		w.notify(cfg, agent, folder, file)
	}
}

// detectNewFiles returns the absolute paths of files in folder that are *new*
// — present now but not in the set recorded in marker — then records the
// current set so they are not re-reported. A missing or unreadable marker
// baselines: records the current files, reports nothing. This is also the
// first scan after an upgrade from a version that kept the marker in the
// folder; that old file is neither read nor removed. fresh baselines without
// reading the marker (a folder watched again, see State). agentID and
// agentName name the owner in an alert.
//
// "New" is by path, not mtime: appending to or editing an already-seen file does
// NOT fire — only a file whose path we haven't seen before. The seen-set lives
// on disk, so detection survives restarts (a file added while claw was
// stopped is new on the next scan; an edited one is not).
func (w *Watcher) detectNewFiles(agentID, agentName, folder, marker string, fresh bool) []string {
	var seen map[string]bool
	hadMarker := false
	if !fresh {
		seen, hadMarker = readSeen(marker, folder)
	}

	current := make([]string, 0, len(seen))
	walkErr := filepath.WalkDir(folder, func(p string, d os.DirEntry, werr error) error {
		if werr != nil {
			return nil //nolint:nilerr // skip unreadable entries and keep walking
		}
		name := d.Name()
		if d.IsDir() {
			if p != folder && strings.HasPrefix(name, ".") {
				return filepath.SkipDir // skip hidden dirs (.git, etc.)
			}
			return nil
		}
		if strings.HasPrefix(name, ".") {
			return nil
		}
		if rel, rerr := filepath.Rel(folder, p); rerr == nil {
			current = append(current, filepath.ToSlash(rel))
		}
		return nil
	})
	if walkErr != nil {
		logger.WarnCF("folderwatch", "Allowed folder scan aborted", map[string]any{"path": folder, "error": walkErr.Error()})
	}

	if !hadMarker {
		// Baseline: record what's already there, fire nothing.
		w.writeSeen(agentID, agentName, folder, marker, current, true)
		return nil
	}

	var newFiles []string
	for _, rel := range current {
		if !seen[rel] {
			newFiles = append(newFiles, filepath.Join(folder, filepath.FromSlash(rel)))
		}
	}
	if len(newFiles) > 0 {
		// Advance the recorded set to what's present now (also prunes deletions).
		w.writeSeen(agentID, agentName, folder, marker, current, false)
	}
	return newFiles
}

// readSeen loads the recorded set of folder-relative file paths from the
// marker. The bool is false when the marker does not exist yet or is not a
// marker for folder (baseline needed).
func readSeen(marker, folder string) (map[string]bool, bool) {
	data, err := os.ReadFile(marker) //nolint:gosec // marker under the agent's own state folder
	if err != nil {
		return nil, false
	}
	lines := strings.Split(string(data), "\n")
	if len(lines) < 2 || strings.TrimSpace(lines[0]) != markerHeader || lines[1] != folder {
		return nil, false
	}
	set := make(map[string]bool)
	for _, line := range lines[2:] {
		if line = strings.TrimSpace(line); line != "" {
			set[line] = true
		}
	}
	return set, true
}

// writeSeen persists the set of folder-relative file paths to the marker,
// with the modes of the area it lies in (the agents area for a workspace),
// creating its folder as needed. baseline says no list is saved yet: a
// failure then means no notice is ever sent for the folder, since every scan
// starts over; a later failure only means files may be reported again.
func (w *Watcher) writeSeen(agentID, agentName, folder, marker string, paths []string, baseline bool) {
	sort.Strings(paths)
	body := markerHeader + "\n" + folder + "\n" + strings.Join(paths, "\n") + "\n"
	if err := perms.CurrentPolicy().WriteFile(marker, []byte(body)); err != nil {
		logger.WarnCF("folderwatch", "Failed to save the file list of an allowed folder", map[string]any{"agent": agentName, "folder": folder, "path": marker, "error": err.Error()})
		description := agentName + "'s allowed folder " + folder + ": the same files may be reported again"
		if baseline {
			description = "New-file notices for " + folder + " (" + agentName + ") are not sent: the list of files already there could not be saved"
		}
		w.alerter.Send(alerter.Alert{
			Title:       "Allowed folder file list not saved",
			Description: description,
			Details:     err.Error(),
			EventID:     "allowed-folder:" + agentID + ":" + folder,
		})
	}
}

// notify delivers a single new-file notice to the agent's default channel, the
// same routing a cron job or a live user message gets.
func (w *Watcher) notify(cfg *config.Config, agent *config.AgentConfig, folder, file string) {
	channel, chatID, peerKind, ok := cfg.CronTarget(agent.ID)
	if !ok {
		logger.WarnCF("folderwatch", "agent has no default channel; notification skipped",
			map[string]any{"agent_id": agent.ID, "file": file})
		return
	}
	msg := bus.InboundMessage{
		Channel:  channel,
		SenderID: "folder-notify",
		Internal: true,
		ChatID:   chatID,
		Content:  "New file in " + agent.DisplayName() + "'s allowed folder " + folder + ": " + file,
		Peer:     bus.Peer{Kind: peerKind, ID: chatID},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := w.bus.PublishInbound(ctx, msg); err != nil {
		logger.WarnCF("folderwatch", "failed to publish notification",
			map[string]any{"agent_id": agent.ID, "error": err.Error()})
	}
}
