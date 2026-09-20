// ClawEh - Session store CLI
// License: MIT

// Package sessions provides the `claw sessions` maintenance subcommands.
package sessions

import (
	"fmt"
	"io"
	"os"
	"sort"

	"github.com/PivotLLM/ctxengine/session"
	"github.com/spf13/cobra"

	"github.com/PivotLLM/ClawEh/internal"
	"github.com/PivotLLM/ClawEh/internal/pidfile"
)

// NewSessionsCommand builds the `claw sessions` command group.
func NewSessionsCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "sessions",
		Short: "Maintain session stores across all assistants",
	}
	cmd.AddCommand(newMigrateCommand())
	return cmd
}

func newMigrateCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "migrate",
		Short: "Fold legacy .jsonl/.meta.json session files into each session's archive DB",
		Long: "Folds every <key>.jsonl + <key>.meta.json pair in each assistant's " +
			"sessions directory into that session's <key>.archive.db, which now " +
			"holds the live window and the session state alongside the archive. " +
			"Sources are renamed *.migrated and can be deleted once verified. " +
			"Sessions already migrated are skipped.\n\n" +
			"Run once, with the service stopped; the command refuses to run " +
			"while the gateway is up.",
		Args: cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			return runMigrate()
		},
	}
}

func runMigrate() error {
	cfg, err := internal.LoadConfig()
	if err != nil {
		return err
	}
	if pid, running := pidfile.Read(cfg.DataDir()); running {
		return fmt.Errorf("%s is running (pid %d); stop the service before migrating sessions", internal.BinaryName, pid)
	}
	return migrateDirs(os.Stdout, cfg.AgentSessionDirs())
}

// migrateDirs runs the migration over every existing sessions directory,
// printing one line per session and a totals line. It returns an error when
// any session or directory failed, so the process exits non-zero.
func migrateDirs(w io.Writer, dirs []string) error {
	var migrated, skipped, failed int
	for _, dir := range dirs {
		if _, err := os.Stat(dir); err != nil {
			continue // no sessions directory yet — nothing to migrate
		}
		report, err := session.MigrateJSONL(dir)
		if err != nil {
			if _, werr := fmt.Fprintf(w, "%s: error: %v\n", dir, err); werr != nil {
				return werr
			}
			failed++
			continue
		}
		for _, key := range sorted(report.Migrated) {
			if _, werr := fmt.Fprintf(w, "%s: migrated\n", key); werr != nil {
				return werr
			}
		}
		for _, key := range sorted(report.Skipped) {
			if _, werr := fmt.Fprintf(w, "%s: skipped (already migrated)\n", key); werr != nil {
				return werr
			}
		}
		errKeys := make([]string, 0, len(report.Errors))
		for key := range report.Errors {
			errKeys = append(errKeys, key)
		}
		for _, key := range sorted(errKeys) {
			if _, werr := fmt.Fprintf(w, "%s: error: %v\n", key, report.Errors[key]); werr != nil {
				return werr
			}
		}
		migrated += len(report.Migrated)
		skipped += len(report.Skipped)
		failed += len(report.Errors)
	}

	if _, werr := fmt.Fprintf(w, "Migrated %d, skipped %d, errors %d.\n", migrated, skipped, failed); werr != nil {
		return werr
	}
	if failed > 0 {
		return fmt.Errorf("%d session(s) failed to migrate", failed)
	}
	return nil
}

func sorted(keys []string) []string {
	out := append([]string(nil), keys...)
	sort.Strings(out)
	return out
}
