package gateway

import (
	"context"
	"time"

	"github.com/tenebris-tech/alerter"

	"github.com/PivotLLM/ClawEh/config"
	"github.com/PivotLLM/ClawEh/internal/backup"
	"github.com/PivotLLM/ClawEh/logger"
)

// startBackupScheduler runs the optional nightly backup. It ticks once a minute
// and, when backup is enabled and the local clock reaches the configured HH:MM,
// writes the backup archive (once per day). getConfig is read
// live each tick so toggling the feature, the time, or retention takes effect
// without a restart. Returns a stop function.
func startBackupScheduler(getConfig func() *config.Config, configPath string, a alerter.Alerter) func() {
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
				res, err := backup.RunForConfig(cfg, configPath, now, backup.Options{Alerter: a}) //nolint:contextcheck // same code path as `claw backup`, which has no context; the quick_check inside runs on its own busy_timeout-bounded connection
				if err != nil {
					logger.ErrorCF("backup", "nightly backup failed", map[string]any{"error": err.Error()})
					a.Send(alerter.Alert{
						Title:       "Nightly backup failed",
						Description: "the backup archive was not written; nothing retries before tomorrow",
						Details:     err.Error(),
						EventID:     "backup",
					})
					continue
				}
				logger.InfoCF("backup", "nightly backup complete", map[string]any{
					"archive": res.Archive, "bytes": res.Bytes, "files": res.Files, "skipped": len(res.Skipped),
				})
			}
		}
	}()
	return cancel
}
