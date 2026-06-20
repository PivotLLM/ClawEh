// Package backup snapshots key configuration files (config.json and the cron
// jobs file) into <data dir>/backup/YYYYMMDD/, and prunes old day-folders. It is
// driven on a nightly schedule by the gateway and on demand via the WebUI.
package backup

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/PivotLLM/ClawEh/pkg/config"
	"github.com/PivotLLM/ClawEh/pkg/logger"
)

// dateLayout is the per-day backup folder name (and the prune parser).
const dateLayout = "20060102"

// stampLayout suffixes each backed-up file with the run's date+time
// (YYYYMMDD-HHMMSS) so repeated (especially manual) runs on the same day never
// overwrite one another — e.g. config.json.20260619-030500.
const stampLayout = "20060102-150405"

// Run copies the given source files into destRoot/<YYYYMMDD>/ (the folder for
// now's local date), creating the folder if needed. Each file is written as
// "<name>.<YYYYMMDD-HHMMSS>" so same-day runs don't collide. A missing source
// file is skipped (not an error) so a fresh install with no cron jobs still
// backs up the config. Returns the day-folder path and the number of files copied.
func Run(destRoot string, now time.Time, sources map[string]string) (string, int, error) {
	day := filepath.Join(destRoot, now.Format(dateLayout))
	if err := os.MkdirAll(day, 0o755); err != nil {
		return "", 0, fmt.Errorf("backup: create %s: %w", day, err)
	}
	stamp := now.Format(stampLayout)
	copied := 0
	for src, name := range sources {
		data, err := os.ReadFile(src)
		if err != nil {
			if os.IsNotExist(err) {
				continue // nothing to back up for this file yet
			}
			return day, copied, fmt.Errorf("backup: read %s: %w", src, err)
		}
		dest := filepath.Join(day, name+"."+stamp)
		if err := os.WriteFile(dest, data, 0o600); err != nil {
			return day, copied, fmt.Errorf("backup: write %s: %w", dest, err)
		}
		copied++
	}
	return day, copied, nil
}

// RunForConfig snapshots config.json (at configPath) and the cron jobs file into
// <data dir>/backup/<YYYYMMDD>/, then prunes folders past the retention window.
// Used by both the nightly scheduler and the manual WebUI trigger. Returns the
// day-folder and the number of files copied.
func RunForConfig(cfg *config.Config, configPath string, now time.Time) (string, int, error) {
	destRoot := filepath.Join(cfg.DataDir(), "backup")
	sources := map[string]string{
		configPath: "config.json",
		filepath.Join(cfg.CronPath(), "jobs.json"): "jobs.json",
	}
	day, copied, err := Run(destRoot, now, sources)
	if err != nil {
		return day, copied, err
	}
	if _, perr := Prune(destRoot, cfg.Backup.BackupRetainDays(), now); perr != nil {
		logger.WarnCF("backup", "prune failed", map[string]any{"error": perr.Error()})
	}
	return day, copied, nil
}

// Prune removes backup day-folders older than retainDays (by their YYYYMMDD
// name) under destRoot. retainDays <= 0 disables pruning. Returns the number of
// folders removed. Non-date entries are left untouched.
func Prune(destRoot string, retainDays int, now time.Time) (int, error) {
	if retainDays <= 0 {
		return 0, nil
	}
	entries, err := os.ReadDir(destRoot)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, fmt.Errorf("backup prune: read %s: %w", destRoot, err)
	}
	cutoff := now.AddDate(0, 0, -retainDays)
	cutoffDay := time.Date(cutoff.Year(), cutoff.Month(), cutoff.Day(), 0, 0, 0, 0, cutoff.Location())
	removed := 0
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		d, perr := time.ParseInLocation(dateLayout, e.Name(), now.Location())
		if perr != nil {
			continue // not a backup day-folder
		}
		if d.Before(cutoffDay) {
			if err := os.RemoveAll(filepath.Join(destRoot, e.Name())); err != nil {
				logger.WarnCF("backup", "failed to prune old backup", map[string]any{
					"folder": e.Name(), "error": err.Error(),
				})
				continue
			}
			removed++
		}
	}
	return removed, nil
}
