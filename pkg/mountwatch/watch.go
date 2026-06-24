// ClawEh
// License: MIT

// Package mountwatch polls notify-enabled external mounts and tells each owning
// agent (on its default channel, cron-style) when a new file appears.
package mountwatch

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/PivotLLM/ClawEh/pkg/bus"
	"github.com/PivotLLM/ClawEh/pkg/config"
	"github.com/PivotLLM/ClawEh/pkg/global"
	"github.com/PivotLLM/ClawEh/pkg/logger"
)

// markerFile is the per-mount watermark. Its mtime marks the last time the mount
// was baselined or fired; a file newer than the marker is "new". Persisting it on
// disk makes detection restart-safe (a file written while claw is stopped is not
// missed). It is touched only on first-create (baseline) and after firing, so the
// scan adds almost no churn.
const markerFile = ".claw"

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
// files newer than the .claw watermark, then advances the watermark so they are
// not re-reported. A missing marker baselines (creates it, reports nothing).
func detectNewFiles(mountName, mountPath string) []string {
	marker := filepath.Join(mountPath, markerFile)
	mi, err := os.Stat(marker)
	if err != nil {
		// No marker yet → baseline: create it and fire nothing for existing files.
		touch(marker)
		return nil
	}
	watermark := mi.ModTime()

	var newFiles []string
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
		info, ierr := d.Info()
		if ierr != nil {
			return nil
		}
		if info.ModTime().After(watermark) {
			if rel, rerr := filepath.Rel(mountPath, p); rerr == nil {
				newFiles = append(newFiles, filepath.ToSlash(filepath.Join(mountName, rel)))
			}
		}
		return nil
	})

	if len(newFiles) > 0 {
		touch(marker) // advance the watermark so these are not re-fired
	}
	return newFiles
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

// touch sets the file's mtime to now, creating it if absent.
func touch(path string) {
	now := time.Now()
	if err := os.Chtimes(path, now, now); err != nil {
		if f, e := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0o600); e == nil {
			_ = f.Close()
		}
	}
}
