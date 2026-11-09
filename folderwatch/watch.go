// ClawEh
// License: MIT

// Package folderwatch polls notify-enabled allowed folders and tells each
// owning agent (on its default channel, cron-style) when a new file appears.
package folderwatch

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/tenebris-tech/alerter"

	"github.com/PivotLLM/ClawEh/bus"
	"github.com/PivotLLM/ClawEh/config"
	"github.com/PivotLLM/ClawEh/fileutil"
	"github.com/PivotLLM/ClawEh/global"
	"github.com/PivotLLM/ClawEh/internal/perms"
	"github.com/PivotLLM/ClawEh/logger"
)

// markerFile records the set of file paths the watcher has already seen in a
// folder (one per line, relative to the folder, after markerHeader). A file present now but absent from the set is "new"; an
// edited/appended file is not (its path is already recorded). Persisting it on
// disk makes detection restart-safe and is rewritten only on baseline and when a
// new file fires, so the scan adds almost no churn. Same name the file tools hide
// from agents.
const markerFile = global.FolderMarkerFile

// markerHeader is the first line of a marker. A marker without it was written
// for a mount (entries prefixed with the mount's name) and is read as missing,
// so the first scan after the upgrade records the folder instead of reporting
// every file in it.
const markerHeader = "# claw allowed folder v1"

// Watcher periodically scans every notify-enabled allowed folder across all
// agents.
type Watcher struct {
	cfg      func() *config.Config
	bus      *bus.MessageBus
	interval time.Duration
	alerter  alerter.Alerter
	stop     chan struct{}
	stopOnce sync.Once
	wg       sync.WaitGroup
}

// New builds a Watcher. interval <= 0 uses global.FolderNotifyIntervalSeconds.
// a receives operator alerts for scan and marker failures; nil means none.
func New(cfgGetter func() *config.Config, b *bus.MessageBus, interval time.Duration, a alerter.Alerter) *Watcher {
	if interval <= 0 {
		interval = time.Duration(global.FolderNotifyIntervalSeconds) * time.Second
	}
	if a == nil {
		a = alerter.Nop{}
	}
	return &Watcher{cfg: cfgGetter, bus: b, interval: interval, alerter: a, stop: make(chan struct{})}
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

func (w *Watcher) tick() {
	cfg := w.cfg()
	if cfg == nil {
		return
	}
	for i := range cfg.Agents.List {
		agent := &cfg.Agents.List[i]
		seen := map[string]bool{}
		for _, f := range agent.AllowedFolders {
			path := strings.TrimSpace(f.Path)
			if !f.Notify || !filepath.IsAbs(path) {
				continue
			}
			path = filepath.Clean(path)
			if seen[path] {
				continue
			}
			seen[path] = true
			if info, serr := os.Stat(path); serr != nil || !info.IsDir() {
				continue
			}
			w.scanFolder(cfg, agent, path)
		}
	}
}

// scanFolder detects new files in one folder via the .claw watermark and
// notifies the owning agent for each.
func (w *Watcher) scanFolder(cfg *config.Config, agent *config.AgentConfig, folder string) {
	for _, file := range w.detectNewFiles(agent.DisplayName(), folder) {
		logger.InfoCF("folderwatch", "new file detected in allowed folder", map[string]any{
			"agent_id": agent.ID,
			"folder":   folder,
			"file":     file,
		})
		w.notify(cfg, agent, folder, file)
	}
}

// detectNewFiles returns the absolute paths of files in folder that are *new*
// — present now but not in the set recorded in the .claw marker — then records
// the current set so they are not re-reported. A missing marker (or one
// written before allowed folders, see markerHeader) baselines: records the
// current files, reports nothing. agent names the owner in an alert.
//
// "New" is by path, not mtime: appending to or editing an already-seen file does
// NOT fire — only a file whose path we haven't seen before. The seen-set lives in
// .claw on disk, so detection survives restarts (a file added while claw was
// stopped is new on the next scan; an edited one is not).
func (w *Watcher) detectNewFiles(agent, folder string) []string {
	marker := filepath.Join(folder, markerFile)
	seen, hadMarker := readSeen(marker)

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
		if name == markerFile || strings.HasPrefix(name, ".") {
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
		w.writeSeen(agent, folder, marker, current)
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
		w.writeSeen(agent, folder, marker, current)
	}
	return newFiles
}

// readSeen loads the recorded set of folder-relative file paths from the
// marker. The bool is false when the marker does not exist yet, or predates
// allowed folders (baseline needed).
func readSeen(marker string) (map[string]bool, bool) {
	data, err := os.ReadFile(marker) //nolint:gosec // marker file under the configured allowed folder
	if err != nil {
		return nil, false
	}
	header, body, _ := strings.Cut(string(data), "\n")
	if strings.TrimSpace(header) != markerHeader {
		return nil, false
	}
	set := make(map[string]bool)
	for line := range strings.SplitSeq(body, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			set[line] = true
		}
	}
	return set, true
}

// writeSeen persists the set of folder-relative file paths to the marker.
func (w *Watcher) writeSeen(agent, folder, marker string, paths []string) {
	sort.Strings(paths)
	body := markerHeader + "\n" + strings.Join(paths, "\n") + "\n"
	if err := fileutil.WriteFileExact(marker, []byte(body), perms.CurrentPolicy().ModesFor(marker).File); err != nil {
		logger.WarnCF("folderwatch", "Failed to write seen-files marker", map[string]any{"agent": agent, "path": marker, "error": err.Error()})
		w.alerter.Send(alerter.Alert{
			Title:       "Allowed folder marker not written",
			Description: agent + "'s allowed folder " + folder + ": the same files may be reported again",
			Details:     err.Error(),
			EventID:     "allowed-folder:" + folder,
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
