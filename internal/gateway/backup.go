package gateway

import (
	"context"
	"time"

	"github.com/PivotLLM/ClawEh/internal/backup"
	"github.com/PivotLLM/ClawEh/pkg/config"
	"github.com/PivotLLM/ClawEh/pkg/logger"
)

// startBackupScheduler runs the optional nightly backup. It ticks once a minute
// and, when backup is enabled and the local clock reaches the configured HH:MM,
// snapshots config.json + the cron jobs file (once per day). getConfig is read
// live each tick so toggling the feature, the time, or retention takes effect
// without a restart. Returns a stop function.
func startBackupScheduler(getConfig func() *config.Config, configPath string) func() {
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		lastRunDay := "" // YYYYMMDD of the last successful run; guards once-per-day
		for {
			select {
			case <-ctx.Done():
				return
			case now := <-ticker.C:
				cfg := getConfig()
				if cfg == nil || !cfg.Backup.IsEnabled() {
					continue
				}
				hour, minute := cfg.Backup.BackupAt()
				if now.Hour() != hour || now.Minute() != minute {
					continue
				}
				day := now.Format("20060102")
				if day == lastRunDay {
					continue
				}
				lastRunDay = day
				dir, copied, err := backup.RunForConfig(cfg, configPath, now)
				if err != nil {
					logger.ErrorCF("backup", "nightly backup failed", map[string]any{"error": err.Error()})
					continue
				}
				logger.InfoCF("backup", "nightly backup complete", map[string]any{"folder": dir, "files": copied})
			}
		}
	}()
	return cancel
}
