// ClawEh
// License: MIT

// Package mountwatch polls notify-enabled external mounts and tells each owning
// agent (on its default channel, cron-style) when a new file appears.
package mountwatch

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/PivotLLM/ClawEh/pkg/bus"
	"github.com/PivotLLM/ClawEh/pkg/config"
	"github.com/PivotLLM/ClawEh/pkg/global"
	"github.com/PivotLLM/ClawEh/pkg/logger"
)

// markerFile records the set of file paths the watcher has already seen in a
// mount (one per line). A file present now but absent from the set is "new"; an
// edited/appended file is not (its path is already recorded). Persisting it on
// disk makes detection restart-safe and is rewritten only on baseline and when a
// new file fires, so the scan adds almost no churn. Same name the file tools hide
// from agents.
const markerFile = global.MountMarkerFile

// Watcher periodically scans every notify-enabled mount across all agents.
type Watcher struct {
	cfg      func() *config.Config
	bus      *bus.MessageBus
	interval time.Duration
	stop     chan struct{}
	wg       sync.WaitGroup
}

// New builds a Watcher. interval <= 0 uses global.MountNotifyIntervalSeconds.
func New(cfgGetter func() *config.Config, b *bus.MessageBus, interval time.Duration) *Watcher {
	if interval <= 0 {
		interval = time.Duration(global.MountNotifyIntervalSeconds) * time.Second
	}
	return &Watcher{cfg: cfgGetter, bus: b, interval: interval, stop: make(chan struct{})}
}

func (w *Watcher) Start() {
	w.wg.Add(1)
	go w.run()
}

func (w *Watcher) Stop() {
	if w == nil {
		return
	}
	close(w.stop)
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
		for _, mc := range agent.Mounts {
			if !mc.Notify {
				continue
			}
			if err := config.ValidateMountName(mc.Name); err != nil {
				continue
			}
			abs, err := filepath.Abs(strings.TrimSpace(mc.Path))
			if err != nil {
				continue
			}
			if info, serr := os.Stat(abs); serr != nil || !info.IsDir() {
				continue
			}
			w.scanMount(cfg, agent.ID, mc.Name, abs)
		}
	}
}

// scanMount detects new files in one mount via the .claw watermark and notifies
// the owning agent for each.
func (w *Watcher) scanMount(cfg *config.Config, agentID, mountName, mountPath string) {
	for _, rel := range detectNewFiles(mountName, mountPath) {
		logger.InfoCF("mountwatch", "new file detected in mount", map[string]any{
			"agent_id": agentID,
			"mount":    mountName,
			"file":     rel,
		})
		w.notify(cfg, agentID, rel)
	}
}

// detectNewFiles returns the mount-relative paths (e.g. "notes/sub/x.md") of
// files that are *new* — present now but not in the set recorded in the .claw
// marker — then records the current set so they are not re-reported. A missing
// marker baselines (records the current files, reports nothing).
//
// "New" is by path, not mtime: appending to or editing an already-seen file does
// NOT fire — only a file whose path we haven't seen before. The seen-set lives in
// .claw on disk, so detection survives restarts (a file added while claw was
// stopped is new on the next scan; an edited one is not).
func detectNewFiles(mountName, mountPath string) []string {
	marker := filepath.Join(mountPath, markerFile)
	seen, hadMarker := readSeen(marker)

	current := make([]string, 0, len(seen))
	_ = filepath.WalkDir(mountPath, func(p string, d os.DirEntry, werr error) error {
		if werr != nil {
			return nil
		}
		name := d.Name()
		if d.IsDir() {
			if p != mountPath && strings.HasPrefix(name, ".") {
				return filepath.SkipDir // skip hidden dirs (.git, etc.)
			}
			return nil
		}
		if name == markerFile || strings.HasPrefix(name, ".") {
			return nil
		}
		if rel, rerr := filepath.Rel(mountPath, p); rerr == nil {
			current = append(current, filepath.ToSlash(filepath.Join(mountName, rel)))
		}
		return nil
	})

	if !hadMarker {
		// Baseline: record what's already there, fire nothing.
		writeSeen(marker, current)
		return nil
	}

	var newFiles []string
	for _, rel := range current {
		if !seen[rel] {
			newFiles = append(newFiles, rel)
		}
	}
	if len(newFiles) > 0 {
		// Advance the recorded set to what's present now (also prunes deletions).
		writeSeen(marker, current)
	}
	return newFiles
}

// readSeen loads the recorded set of mount-relative file paths from the marker.
// The bool is false when the marker does not exist yet (baseline needed).
func readSeen(marker string) (map[string]bool, bool) {
	data, err := os.ReadFile(marker)
	if err != nil {
		return nil, false
	}
	set := make(map[string]bool)
	for _, line := range strings.Split(string(data), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			set[line] = true
		}
	}
	return set, true
}

// writeSeen persists the set of mount-relative file paths to the marker.
func writeSeen(marker string, paths []string) {
	sort.Strings(paths)
	_ = os.WriteFile(marker, []byte(strings.Join(paths, "\n")+"\n"), 0o600)
}

// notify delivers a single new-file notice to the agent's default channel, the
// same routing a cron job or a live user message gets.
func (w *Watcher) notify(cfg *config.Config, agentID, relPath string) {
	channel, chatID, peerKind, ok := cfg.CronTarget(agentID)
	if !ok {
		logger.WarnCF("mountwatch", "agent has no default channel; notification skipped",
			map[string]any{"agent_id": agentID, "file": relPath})
		return
	}
	msg := bus.InboundMessage{
		Channel:  channel,
		SenderID: "mount-notify",
		ChatID:   chatID,
		Content:  "A new file is available in a mounted folder: " + relPath,
		Peer:     bus.Peer{Kind: peerKind, ID: chatID},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := w.bus.PublishInbound(ctx, msg); err != nil {
		logger.WarnCF("mountwatch", "failed to publish notification",
			map[string]any{"agent_id": agentID, "error": err.Error()})
	}
}

