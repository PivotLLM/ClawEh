package gateway

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"time"

	"github.com/PivotLLM/ClawEh/pkg/logger"
)

// datedLogRe matches a rolled daily log such as "20260616-claw.log" or
// "20260616-error.log". The active claw.log / error.log have no date prefix and
// are never matched, nor are files under logs/dumps/.
var datedLogRe = regexp.MustCompile(`^\d{8}-.+\.log$`)

// startLogRotation rolls the active logs at local midnight, rolls immediately on
// startup when the active claw.log was last written before today (covering the
// case where the gateway was down at midnight), and prunes rolled archives older
// than retentionDays. retentionDays <= 0 keeps archives forever. The goroutine
// exits when ctx is cancelled.
func startLogRotation(ctx context.Context, logPath string, retentionDays int) {
	dir := filepath.Dir(logPath)

	// Startup roll: archive claw.log now if its last write predates today.
	if fi, err := os.Stat(logPath); err == nil && fi.Size() > 0 {
		if dateOf(fi.ModTime()).Before(dateOf(time.Now())) {
			_ = logger.RollLogFile()
			pruneOldLogs(dir, retentionDays, time.Now())
		}
	}

	go func() {
		for {
			timer := time.NewTimer(time.Until(nextMidnight(time.Now())))
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
				_ = logger.RollLogFile()
				pruneOldLogs(dir, retentionDays, time.Now())
			}
		}
	}()
}

// dateOf returns t truncated to local midnight (date only).
func dateOf(t time.Time) time.Time {
	y, m, d := t.Date()
	return time.Date(y, m, d, 0, 0, 0, 0, t.Location())
}

// nextMidnight returns the next local-midnight boundary after now.
func nextMidnight(now time.Time) time.Time {
	return dateOf(now).AddDate(0, 0, 1)
}

// pruneOldLogs deletes rolled daily logs in dir whose date is older than
// retentionDays before now. A retentionDays of 0 (or less) keeps everything.
func pruneOldLogs(dir string, retentionDays int, now time.Time) {
	if retentionDays <= 0 {
		return
	}
	cutoff := dateOf(now).AddDate(0, 0, -retentionDays)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() || !datedLogRe.MatchString(e.Name()) {
			continue
		}
		d, err := time.ParseInLocation("20060102", e.Name()[:8], time.Local)
		if err != nil {
			continue
		}
		if d.Before(cutoff) {
			_ = os.Remove(filepath.Join(dir, e.Name()))
		}
	}
}
